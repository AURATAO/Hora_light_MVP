// Server entrypoint for Hora (Version A: backend ↔ Supabase Postgres).
// - Auth: validates Supabase JWT via JWKS (RS256).
// - DB: connects directly to Supabase Postgres using pgxpool.
// - Scope: profiles, tasks, worklogs CRUD and basic business flows.
// Required ENV:
//   SUPABASE_PROJECT_URL=https://<ref>.supabase.co
//   SUPABASE_DB_URL=postgres://postgres:<password>@db.<ref>.supabase.co:5432/postgres?sslmode=require

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// Fetch JWKS from Supabase and auto-refresh. This validates access tokens
// issued by your project (RS256). If the issuer doesn't match, we reject.

var jwks *keyfunc.JWKS
var db *pgxpool.Pool

type Task struct {
	ID                string     `json:"id"` // ← 原本是 string，改成 int64
	Title             string     `json:"title"`
	Description       string     `json:"description"`
	Category          string     `json:"category"`
	LocationText      string     `json:"location_text"`
	EstimatedMinutes  int        `json:"estimated_minutes"`
	PrepayAmountCents int        `json:"prepay_amount_cents"`
	IsImmediate       bool       `json:"is_immediate"`
	ScheduledAt       *time.Time `json:"scheduled_at,omitempty"`
	Requester         string     `json:"requester"` // Supabase user UUID
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	AssignedTo        string     `json:"assigned_to"`
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

type Profile struct {
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	City      string    `json:"city"`
	AvatarURL string    `json:"avatar_url"`
	Bio       string    `json:"bio"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkLog struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"task_id"`
	User      string     `json:"user"`
	Start     time.Time  `json:"start"`
	End       *time.Time `json:"end,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func main() {
	_ = godotenv.Load()

	projectURL := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
	jwksURL := strings.TrimSpace(os.Getenv("SUPABASE_JWKS_URL"))
	if jwksURL == "" && projectURL != "" {
		jwksURL = projectURL + "/auth/v1/keys"
	}
	if jwksURL == "" {
		log.Fatal("SUPABASE_JWKS_URL is not set (hint: set SUPABASE_PROJECT_URL=https://<ref>.supabase.co)")
	}
	log.Printf("[auth] using JWKS URL: %s", jwksURL)

	var err error
	jwks, err = keyfunc.Get(jwksURL, keyfunc.Options{
		RefreshInterval: time.Hour, // 定期自動更新金鑰
		RefreshTimeout:  10 * time.Second,
		Ctx:             context.Background(),
		RefreshErrorHandler: func(err error) {
			log.Printf("[jwks] refresh error: %v", err)
		},
	})
	if err != nil {
		log.Fatalf("failed to init JWKS: %v", err)
	}
	//DB init
	// Initialize pgx pool. Keep MaxConns conservative on small instances.
	// Tip: add `?sslmode=require` in SUPABASE_DB_URL for production.
	// Initialize Postgres (Supabase) connection pool.
	// Initialize Postgres (Supabase) via Transaction Pooler (IPv4 proxied).
	dbURL := strings.TrimSpace(os.Getenv("SUPABASE_DB_URL"))
	if dbURL == "" {
		log.Fatal("SUPABASE_DB_URL is not set")
	}

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("pgx ParseConfig error: %v", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour

	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	db, err = pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("pgxpool.New error: %v", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		log.Fatalf("DB ping failed: %v", err)
	}
	log.Println("[db] connected")

	// go cleanupLoop()

	r := gin.Default()

	// CORS: allow local dev and production origins. Adjust before deploying preview domains.

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

	// r.POST("/auth/request-otp", requestOTP)
	// r.POST("/auth/verify", verifyOTP)

	auth := r.Group("/auth")
	auth.Use(authMiddleware())
	auth.GET("/me", me)

	// User Profile

	meAPI := r.Group("/profile")
	meAPI.Use(authMiddleware())
	{
		meAPI.GET("", getMyProfile)
		meAPI.PATCH("", patchMyProfile)
	}

	tasksAPI := r.Group("/tasks")
	tasksAPI.Use(authMiddleware())
	{
		tasksAPI.POST("", createTask)
		tasksAPI.GET("", listMyTasks)
		tasksAPI.GET("/:id", getTask)
		tasksAPI.PATCH("/:id", updateTask) // ← 編輯

		tasksAPI.GET("/available", listAvailableTasks)
		tasksAPI.GET("/assigned", listAssignedTasks)
		tasksAPI.GET("/posted", listMyTasks) // alias
		tasksAPI.GET("/done", listDoneTasks)
		tasksAPI.GET("/posted/closed", listMyPostedClosed) // 我發的已完成/取消（可選）

		tasksAPI.POST("/:id/accept", acceptTask)     // 接單
		tasksAPI.POST("/:id/complete", completeTask) // 完成

		// ✅ 新增打卡與查詢工時
		tasksAPI.POST("/:id/clock-in", clockIn)
		tasksAPI.POST("/:id/clock-out", clockOut)
		tasksAPI.GET("/:id/worklogs", getWorklogs)
	}

	addr := ":8080"
	log.Printf("listening on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}

}

func me(c *gin.Context) {
	uid := c.GetString("uid")
	email := c.GetString("email")
	c.JSON(http.StatusOK, gin.H{
		"id":    uid,
		"email": email,
		"name":  deriveName(email),
	})
}

// Verify "Bearer <JWT>" using JWKS and enforce issuer = <PROJECT_URL>/auth/v1.
// Exposes: c.Set("uid") = sub (Supabase user UUID), c.Set("email") if present.

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authz := c.GetHeader("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		tokenStr := strings.TrimPrefix(authz, "Bearer ")

		// 依 alg 選擇驗證方式
		keyfunc := func(t *jwt.Token) (interface{}, error) {
			alg := t.Method.Alg()
			switch alg {
			case "HS256", "HS384", "HS512":
				secret := strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET"))
				if secret == "" {
					return nil, fmt.Errorf("SUPABASE_JWT_SECRET is not set")
				}
				return []byte(secret), nil
			case "RS256", "RS384", "RS512":
				if jwks == nil {
					return nil, fmt.Errorf("jwks not initialized")
				}
				return jwks.Keyfunc(t)
			default:
				return nil, fmt.Errorf("unsupported alg: %s", alg)
			}
		}

		token, err := jwt.Parse(tokenStr, keyfunc)
		if err != nil || !token.Valid {
			log.Printf("[auth] invalid token: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid claims"})
			return
		}

		// Issuer 檢查
		expIss := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/") + "/auth/v1"
		if iss := fmt.Sprint(claims["iss"]); expIss != "" && iss != expIss {
			log.Printf("[auth] invalid issuer: got=%s want=%s", iss, expIss)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid issuer"})
			return
		}

		c.Set("claims", claims)
		c.Set("uid", fmt.Sprint(claims["sub"]))
		if email, _ := claims["email"].(string); email != "" {
			c.Set("email", email)
		}
		c.Next()
	}
}

// -------- Auth handlers (OTP via email) --------
// deriveName: naive display name from email local-part; replace with real profile later.
// e.g. "jane.doe@x.com" -> "Jane Doe"

func deriveName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return strings.Title(strings.ReplaceAll(email[:i], ".", " "))
	}
	return email
}

// -------- Profile handlers --------
// getMyProfile: lazy-create profile if missing (idempotent).
// patchMyProfile: upsert via ON CONFLICT(email).

func getMyProfile(c *gin.Context) {
	email := c.GetString("email")
	ctx := c.Request.Context()

	var p Profile
	err := db.QueryRow(ctx, `
    select email, name, phone, city, avatar_url, bio, created_at, updated_at
    from public.profiles where email = $1
  `, email).Scan(&p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		// 不存在就建一筆預設
		now := time.Now()
		_, err2 := db.Exec(ctx, `
      insert into public.profiles(email,name,phone,city,avatar_url,bio,created_at,updated_at)
      values ($1,$2,'','','','',$3,$3)
    `, email, deriveName(email), now)
		if err2 != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		p = Profile{
			Email: email, Name: deriveName(email),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	c.JSON(http.StatusOK, p)
}

func patchMyProfile(c *gin.Context) {
	email := c.GetString("email")
	var in struct {
		Name      *string `json:"name"`
		Phone     *string `json:"phone"`
		City      *string `json:"city"`
		AvatarURL *string `json:"avatar_url"`
		Bio       *string `json:"bio"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	// 讀舊值
	ctx := c.Request.Context()
	var p Profile
	_ = db.QueryRow(ctx, `
    select email, name, phone, city, avatar_url, bio, created_at, updated_at
    from public.profiles where email = $1
  `, email).Scan(&p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.CreatedAt, &p.UpdatedAt)

	// upsert
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Phone != nil {
		p.Phone = strings.TrimSpace(*in.Phone)
	}
	if in.City != nil {
		p.City = strings.TrimSpace(*in.City)
	}
	if in.AvatarURL != nil {
		p.AvatarURL = strings.TrimSpace(*in.AvatarURL)
	}
	if in.Bio != nil {
		p.Bio = strings.TrimSpace(*in.Bio)
	}
	p.Email = email
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.UpdatedAt = time.Now()

	_, err := db.Exec(ctx, `
    insert into public.profiles(email,name,phone,city,avatar_url,bio,created_at,updated_at)
    values ($1,$2,$3,$4,$5,$6,$7,$8)
    on conflict (email) do update
    set name=$2, phone=$3, city=$4, avatar_url=$5, bio=$6, updated_at=$8
  `, p.Email, p.Name, p.Phone, p.City, p.AvatarURL, p.Bio, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// -------- Tasks handlers --------
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
	}
	if in.Category != "task" && in.Category != "companion" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid category"})
		return
	}
	if in.PrepayAmountCents < 0 {
		in.PrepayAmountCents = 0
	}

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

	requester := c.GetString("email")
	ctx := c.Request.Context()
	var id string
	var createdAt time.Time
	err := db.QueryRow(ctx, `
    insert into public.tasks
      (title,description,category,location_text,estimated_minutes,prepay_amount_cents,is_immediate,scheduled_at,requester,status,assigned_to)
    values ($1,$2,$3,$4,$5,$6,$7,$8,$9,'open','')
    returning id, created_at
  `, in.Title, in.Description, in.Category, in.LocationText, in.EstimatedMinutes, in.PrepayAmountCents, in.IsImmediate, when, requester).Scan(&id, &createdAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusCreated, Task{
		ID: id, Title: in.Title, Description: in.Description, Category: in.Category,
		LocationText: in.LocationText, EstimatedMinutes: in.EstimatedMinutes,
		PrepayAmountCents: in.PrepayAmountCents, IsImmediate: in.IsImmediate,
		ScheduledAt: when, Requester: requester, Status: "open", CreatedAt: createdAt, AssignedTo: "",
	})
}

func scanTask(rows interface{ Scan(dest ...any) error }) (Task, error) {
	var t Task
	err := rows.Scan(
		&t.ID, &t.Title, &t.Description, &t.Category, &t.LocationText,
		&t.EstimatedMinutes, &t.PrepayAmountCents, &t.IsImmediate,
		&t.ScheduledAt, &t.Requester, &t.Status, &t.CreatedAt, &t.AssignedTo,
	)
	return t, err
}

func listMyTasks(c *gin.Context) {
	me := c.GetString("email")
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks
    where requester = $1
    order by created_at desc
  `, me)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func getTask(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	row := db.QueryRow(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks where id=$1
  `, id)
	t, err := scanTask(row)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, t)
}

func updateTask(c *gin.Context) {
	id := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	// 檢查擁有者 & 狀態
	var requester, status string
	if err := db.QueryRow(ctx, `select requester, status from public.tasks where id=$1`, id).Scan(&requester, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requester != me {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your task"})
		return
	}
	if status != "open" {
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

	_, err := db.Exec(ctx, `
    update public.tasks
    set title=$1, description=$2, category=$3, location_text=$4,
        estimated_minutes=$5, prepay_amount_cents=$6, is_immediate=$7, scheduled_at=$8
    where id=$9
  `, in.Title, in.Description, in.Category, in.LocationText, in.EstimatedMinutes, in.PrepayAmountCents, in.IsImmediate, when, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	getTask(c)
}

func listAvailableTasks(c *gin.Context) {
	me := c.GetString("email")
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks
    where status='open' and requester <> $1 and assigned_to = ''
    order by created_at desc
  `, me)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listAssignedTasks(c *gin.Context) {
	me := c.GetString("email")
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks
    where assigned_to = $1 and status='open'
    order by created_at desc
  `, me)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listDoneTasks(c *gin.Context) {
	me := c.GetString("email")
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks
    where assigned_to = $1 and status='completed'
    order by created_at desc
  `, me)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listMyPostedClosed(c *gin.Context) {
	me := c.GetString("email")
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,requester,status,created_at,assigned_to
    from public.tasks
    where requester = $1 and status in ('completed','cancelled')
    order by created_at desc
  `, me)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

// A user cannot accept their own task. Only open & unassigned tasks can be accepted.

func acceptTask(c *gin.Context) {
	id := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	var requester, status, assignedTo string
	err := db.QueryRow(ctx, `select requester,status,assigned_to from public.tasks where id=$1`, id).Scan(&requester, &status, &assignedTo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requester == me {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot accept your own task"})
		return
	}
	if status != "open" || assignedTo != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not available"})
		return
	}

	_, err = db.Exec(ctx, `update public.tasks set assigned_to=$1 where id=$2`, me, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	getTask(c)
}

// -------- WorkLog handlers --------
const centsPerMinute = 50 // 0.5 EUR/min

func clockIn(c *gin.Context) {
	taskID := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	var assignedTo, status string
	if err := db.QueryRow(ctx, `select assigned_to,status from public.tasks where id=$1`, taskID).Scan(&assignedTo, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if assignedTo != me {
		c.JSON(http.StatusForbidden, gin.H{"error": "only assignee can clock in"})
		return
	}
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task not open"})
		return
	}

	// 有沒有未結束的打卡
	var exists bool
	_ = db.QueryRow(ctx, `select exists (select 1 from public.worklogs where task_id=$1 and "user"=$2 and end_at is null)`, taskID, me).Scan(&exists)
	if exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already clocked in"})
		return
	}

	var id string
	var createdAt, startAt time.Time
	err := db.QueryRow(ctx, `
    insert into public.worklogs(task_id,"user",start_at)
    values ($1,$2,now())
    returning id, created_at, start_at
  `, taskID, me).Scan(&id, &createdAt, &startAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusCreated, WorkLog{
		ID: id, TaskID: taskID, User: me, Start: startAt, End: nil, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

func clockOut(c *gin.Context) {
	taskID := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	// 找到開著的工時
	var wlID string
	err := db.QueryRow(ctx, `
    select id from public.worklogs where task_id=$1 and "user"=$2 and end_at is null
    order by start_at asc limit 1
  `, taskID, me).Scan(&wlID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no active session"})
		return
	}

	// 更新 end_at
	var startAt, endAt, createdAt, updatedAt time.Time
	err = db.QueryRow(ctx, `
    update public.worklogs set end_at=now(), updated_at=now()
    where id=$1
    returning start_at, end_at, created_at, updated_at
  `, wlID).Scan(&startAt, &endAt, &createdAt, &updatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, WorkLog{
		ID: wlID, TaskID: taskID, User: me, Start: startAt, End: &endAt, CreatedAt: createdAt, UpdatedAt: updatedAt,
	})
}

func getWorklogs(c *gin.Context) {
	taskID := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	// 權限：作者或接單者
	var requester, assignedTo string
	if err := db.QueryRow(ctx, `select requester,assigned_to from public.tasks where id=$1`, taskID).Scan(&requester, &assignedTo); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requester != me && assignedTo != me {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	rows, err := db.Query(ctx, `
    select id,task_id,"user",start_at,end_at,created_at,updated_at
    from public.worklogs where task_id=$1 order by start_at asc
  `, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := []WorkLog{}
	for rows.Next() {
		var wl WorkLog
		if err := rows.Scan(&wl.ID, &wl.TaskID, &wl.User, &wl.Start, &wl.End, &wl.CreatedAt, &wl.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		items = append(items, wl)
	}

	// total minutes（向上取整；未結束的不算）
	var totalMin int
	_ = db.QueryRow(ctx, `
    with x as (
      select ceil(extract(epoch from (end_at - start_at))/60.0) as m
      from public.worklogs where task_id=$1 and end_at is not null and end_at > start_at
    )
    select coalesce(sum(greatest(m,1))::int, 0) from x
  `, taskID).Scan(&totalMin)

	var hasOpen bool
	_ = db.QueryRow(ctx, `select exists (select 1 from public.worklogs where task_id=$1 and end_at is null)`, taskID).Scan(&hasOpen)

	c.JSON(http.StatusOK, gin.H{
		"items":            items,
		"total_minutes":    totalMin,
		"total_cost_cents": totalMin * centsPerMinute,
		"has_open":         hasOpen,
	})
}

// Completion rules:
// 1) Requester or assignee can complete.
// 2) Task must be open and assigned.
// 3) No open worklog session left.
// 4) Assignee must have at least one closed worklog.

func completeTask(c *gin.Context) {
	taskID := c.Param("id")
	me := c.GetString("email")
	ctx := c.Request.Context()

	var requester, assignedTo, status string
	if err := db.QueryRow(ctx, `select requester,assigned_to,status from public.tasks where id=$1`, taskID).Scan(&requester, &assignedTo, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requester != me && assignedTo != me {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already closed"})
		return
	}
	if assignedTo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "assignment required before completing"})
		return
	}

	var hasOpen bool
	_ = db.QueryRow(ctx, `select exists (select 1 from public.worklogs where task_id=$1 and end_at is null)`, taskID).Scan(&hasOpen)
	if hasOpen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clock-out required before completing"})
		return
	}

	var hasClosedByAssignee bool
	_ = db.QueryRow(ctx, `
    select exists (
      select 1 from public.worklogs where task_id=$1 and "user"=$2 and end_at is not null
    )`, taskID, assignedTo).Scan(&hasClosedByAssignee)
	if !hasClosedByAssignee {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one work session is required before completing"})
		return
	}

	_, err := db.Exec(ctx, `update public.tasks set status='completed' where id=$1`, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	getTask(c)
}
