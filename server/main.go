// main.go
// Minimal OTP auth server for local/dev use.
// Endpoints:
//  POST /auth/request-otp  { email }
//  POST /auth/verify       { email, code } -> { token, user }
//  GET  /auth/me           (Bearer token)  -> { id, email, name }
//
// Usage:
//  go mod init hora-auth
//  go get github.com/gin-gonic/gin github.com/gin-contrib/cors github.com/golang-jwt/jwt/v5
//  export JWT_SECRET=supersecret
//  go run .
//  Frontend .env: VITE_API_BASE_URL=http://localhost:8080

package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"sync"
)

var jwtSecret []byte

type otpEntry struct {
	Code   string
	Expire time.Time
}

// naive in-memory store; fine for dev
var otpStore = map[string]otpEntry{}

type Task struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	Description       string     `json:"description"`
	Category          string     `json:"category"`      // "task" | "companion"
	LocationText      string     `json:"location_text"` // 多地點先用 " | " 串起來
	EstimatedMinutes  int        `json:"estimated_minutes"`
	PrepayAmountCents int        `json:"prepay_amount_cents"`
	IsImmediate       bool       `json:"is_immediate"`
	ScheduledAt       *time.Time `json:"scheduled_at,omitempty"` // RFC3339
	Requester         string     `json:"requester"`
	Status            string     `json:"status"` // "open" | "completed"
	CreatedAt         time.Time  `json:"created_at"`
}

type createTaskInput struct {
	Title             string `json:"title"`
	Description       string `json:"description"`
	Category          string `json:"category"`
	LocationText      string `json:"location_text"`
	EstimatedMinutes  int    `json:"estimated_minutes"`
	PrepayAmountCents int    `json:"prepay_amount_cents"`
	IsImmediate       bool   `json:"is_immediate"`
	ScheduledAt       string `json:"scheduled_at"` // ISO8601 (RFC3339) 或空字串
}

var (
	tasksMu sync.Mutex
	tasks   = map[string]Task{}
)

func main() {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "dev-secret-change-me"
		log.Printf("[warn] JWT_SECRET not set, using default dev secret")
	}
	jwtSecret = []byte(secret)

	go cleanupLoop()

	r := gin.Default()

	// CORS for local + prod origins
	c := cors.Config{
		AllowOrigins: []string{
			"http://localhost:5173",
			"https://horaapp.co",
			"https://app.horaapp.co",
		},
		AllowMethods:     []string{"GET", "POST", "PATCH", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}
	r.Use(cors.New(c))
	r.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	r.POST("/auth/request-otp", requestOTP)
	r.POST("/auth/verify", verifyOTP)

	auth := r.Group("/auth")
	auth.Use(authMiddleware())
	auth.GET("/me", me)

	tasksAPI := r.Group("/tasks")
	tasksAPI.Use(authMiddleware())
	{
		tasksAPI.POST("", createTask)
		tasksAPI.GET("", listMyTasks)
		tasksAPI.GET("/:id", getTask)
		tasksAPI.PATCH("/:id", updateTask) // ← 編輯
	}

	addr := ":8080"
	log.Printf("listening on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}

}

func requestOTP(c *gin.Context) {
	var p struct {
		Email string `json:"email"`
	}
	if err := c.BindJSON(&p); err != nil || p.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if !strings.Contains(email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}

	code := genCode(6)
	otpStore[email] = otpEntry{Code: code, Expire: time.Now().Add(10 * time.Minute)}

	// DEV: print to server logs instead of sending email
	log.Printf("[DEV] OTP for %s => %s (valid 10m)", email, code)

	c.Status(http.StatusNoContent)
}

func verifyOTP(c *gin.Context) {
	var p struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := c.BindJSON(&p); err != nil || p.Email == "" || p.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and code required"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	code := strings.TrimSpace(p.Code)

	entry, ok := otpStore[email]
	if !ok || time.Now().After(entry.Expire) || entry.Code != code {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}
	delete(otpStore, email)

	// issue JWT
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   email, // use email as id for dev
		"email": email,
		"iat":   now.Unix(),
		"exp":   now.Add(30 * 24 * time.Hour).Unix(), // 30 days
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token error"})
		return
	}

	user := map[string]any{
		"id":    email,
		"email": email,
		"name":  deriveName(email),
	}

	c.JSON(http.StatusOK, gin.H{"token": signed, "user": user})
}

func me(c *gin.Context) {
	claims := c.MustGet("claims").(jwt.MapClaims)
	user := map[string]any{
		"id":    claims["sub"],
		"email": claims["email"],
		"name":  deriveName(fmt.Sprint(claims["email"])),
	}
	c.JSON(http.StatusOK, user)
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid claims"})
			return
		}
		c.Set("claims", claims)
		c.Next()
	}
}

func genCode(n int) string {
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		x, _ := rand.Int(rand.Reader, big.NewInt(10))
		b[i] = byte('0' + x.Int64())
	}
	return string(b)
}

func deriveName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return strings.Title(strings.ReplaceAll(email[:i], ".", " "))
	}
	return email
}

// optional: clean expired OTPs
func cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	for range ticker.C {
		now := time.Now()
		for k, v := range otpStore {
			if now.After(v.Expire) {
				delete(otpStore, k)
			}
		}
	}
}

func createTask(c *gin.Context) {
	var in createTaskInput
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Category = strings.TrimSpace(in.Category)
	in.LocationText = strings.TrimSpace(in.LocationText)

	if in.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title required"})
		return
	}
	if in.EstimatedMinutes <= 0 {
		in.EstimatedMinutes = 30
	}
	if in.Category == "" {
		in.Category = "task"
	} else if in.Category != "task" && in.Category != "companion" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid category"})
		return
	}
	if in.PrepayAmountCents < 0 {
		in.PrepayAmountCents = 0
	}

	// 時間處理
	var when *time.Time
	if in.IsImmediate {
		now := time.Now()
		when = &now
	} else if strings.TrimSpace(in.ScheduledAt) != "" {
		t, err := time.Parse(time.RFC3339, in.ScheduledAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scheduled_at must be RFC3339"})
			return
		}
		when = &t
	}

	claims := c.MustGet("claims").(jwt.MapClaims)
	requester := fmt.Sprint(claims["email"])

	id := fmt.Sprintf("T%v", time.Now().UnixNano())
	t := Task{
		ID:                id,
		Title:             in.Title,
		Description:       in.Description,
		Category:          in.Category,
		LocationText:      in.LocationText,
		EstimatedMinutes:  in.EstimatedMinutes,
		PrepayAmountCents: in.PrepayAmountCents,
		IsImmediate:       in.IsImmediate,
		ScheduledAt:       when,
		Requester:         requester,
		Status:            "open",
		CreatedAt:         time.Now(),
	}

	tasksMu.Lock()
	tasks[id] = t
	tasksMu.Unlock()

	c.JSON(http.StatusCreated, t)
}

func listMyTasks(c *gin.Context) {
	claims := c.MustGet("claims").(jwt.MapClaims)
	me := fmt.Sprint(claims["email"])

	out := make([]Task, 0)
	tasksMu.Lock()
	for _, t := range tasks {
		if t.Requester == me {
			out = append(out, t)
		}
	}
	tasksMu.Unlock()

	c.JSON(http.StatusOK, out)
}

func getTask(c *gin.Context) {
	id := c.Param("id")
	tasksMu.Lock()
	t, ok := tasks[id]
	tasksMu.Unlock()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, t)
}

func updateTask(c *gin.Context) {
	id := c.Param("id")
	claims := c.MustGet("claims").(jwt.MapClaims)
	me := fmt.Sprint(claims["email"])

	tasksMu.Lock()
	t, ok := tasks[id]
	tasksMu.Unlock()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if t.Requester != me {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your task"})
		return
	}
	if t.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only open tasks can be edited"})
		return
	}

	var in createTaskInput
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Category = strings.TrimSpace(in.Category)
	in.LocationText = strings.TrimSpace(in.LocationText)
	if in.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title required"})
		return
	}
	if in.EstimatedMinutes <= 0 {
		in.EstimatedMinutes = 30
	}
	if in.Category == "" {
		in.Category = "task"
	}
	if in.Category != "task" && in.Category != "companion" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid category"})
		return
	}
	if in.PrepayAmountCents < 0 {
		in.PrepayAmountCents = 0
	}

	// 時間
	var when *time.Time
	if in.IsImmediate {
		now := time.Now()
		when = &now
	} else if strings.TrimSpace(in.ScheduledAt) != "" {
		tt, err := time.Parse(time.RFC3339, in.ScheduledAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scheduled_at must be RFC3339"})
			return
		}
		when = &tt
	}

	// 套用更新
	t.Title = in.Title
	t.Description = in.Description
	t.Category = in.Category
	t.LocationText = in.LocationText
	t.EstimatedMinutes = in.EstimatedMinutes
	t.PrepayAmountCents = in.PrepayAmountCents
	t.IsImmediate = in.IsImmediate
	t.ScheduledAt = when

	tasksMu.Lock()
	tasks[id] = t
	tasksMu.Unlock()
	c.JSON(http.StatusOK, t)
}
