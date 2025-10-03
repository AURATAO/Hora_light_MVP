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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	notify "hora-auth/internal/notify"
	"os"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

// Fetch JWKS from Supabase and auto-refresh. This validates access tokens
// issued by your project (RS256). If the issuer doesn't match, we reject.

var jwks *keyfunc.JWKS
var db *pgxpool.Pool
var sqldb *sql.DB

type Task struct {
	ID                string     `json:"id"`
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

	// 新：uuid（後端查詢/權限全靠它）
	RequesterID  string  `json:"requester_id"`
	AssignedToID *string `json:"assigned_to_id,omitempty"`
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

type NotificationDTO struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Unread      bool       `json:"unread"`
	ViaEmail    bool       `json:"via_email"`
	EmailSentAt *time.Time `json:"email_sent_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
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
	// 2) 用這個 DSN 開 stdlib 連線
	pgxCfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("pgx ParseConfig (stdlib) error: %v", err)
	}
	pgxCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol // ← 關鍵：走 Simple Protocol
	pgxCfg.StatementCacheCapacity = 0                             // ← 與上面配對：不使用快取

	// 產生給 database/sql 使用的 DSN
	dsn := stdlib.RegisterConnConfig(pgxCfg)

	sqldb, err = sql.Open("pgx", dsn) // 需：import "github.com/jackc/pgx/v5/stdlib"
	if err != nil {
		log.Fatalf("sql.Open error: %v", err)
	}
	if err := sqldb.Ping(); err != nil {
		log.Fatalf("sqldb ping failed: %v", err)
	}
	sqldb.SetMaxOpenConns(8)
	sqldb.SetMaxIdleConns(8)
	sqldb.SetConnMaxLifetime(time.Hour)
	log.Println("[sqldb] connected (simple protocol)")

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
	// 掛上「通知 API」路由（用 sqldb）
	RegisterNotificationRoutes(r, sqldb)

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
	uid := c.GetString("uid")
	if email == "" || uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing uid/email in token"})
		return
	}
	ctx := c.Request.Context()

	var p Profile
	err := db.QueryRow(ctx, `
    select email, name, phone, city, avatar_url, bio, created_at, updated_at
    from public.profiles where email = $1
  `, email).Scan(&p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在就建一筆（⚠️ 帶 id）
			now := time.Now()
			_, err2 := db.Exec(ctx, `
		insert into public.profiles (id, email, name, phone, city, avatar_url, bio, created_at, updated_at)
		values ($1::uuid, $2, $3, '', '', '', '', $4, $4)
	`, uid, email, deriveName(email), now)
			if err2 != nil {
				log.Printf("[profile][get] create default error: %v", err2)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err2.Error()})
				return
			}
			p = Profile{
				Email: email, Name: deriveName(email),
				CreatedAt: now, UpdatedAt: now,
			}
		} else {
			log.Printf("[profile][get] query error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, p)
}

func patchMyProfile(c *gin.Context) {
	email := c.GetString("email")
	uid := c.GetString("uid")
	if email == "" || uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing uid/email in token"})
		return
	}
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
	insert into public.profiles(id,email,name,phone,city,avatar_url,bio,created_at,updated_at)
	values ($1::uuid,$2,$3,$4,$5,$6,$7,$8)
	on conflict (email) do update
	set name=$2, phone=$3, city=$4, avatar_url=$5, bio=$6, updated_at=$8
	`, uid, p.Email, p.Name, p.Phone, p.City, p.AvatarURL, p.Bio, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		log.Printf("[profile][patch] upsert error: %v", err)                // 👈 新增
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()}) // 👈 回傳真錯
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
	} else if s := strings.TrimSpace(in.ScheduledAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scheduled_at must be RFC3339"})
			return
		}
		when = &t
	}

	meUID := c.GetString("uid")
	meEmail := c.GetString("email")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()
	var id string
	var createdAt time.Time
	err := db.QueryRow(ctx, `
		insert into public.tasks
		  (title,description,category,location_text,
		   estimated_minutes,prepay_amount_cents,is_immediate,scheduled_at,
		   requester, requester_id, status, assigned_to, assigned_to_id)
		values
		  ($1,$2,$3,$4,
		   $5,$6,$7,$8,
		   $9, $10::uuid, 'open', '', null)
		returning id, created_at
	`, in.Title, in.Description, in.Category, in.LocationText,
		in.EstimatedMinutes, in.PrepayAmountCents, in.IsImmediate, when,
		meEmail, meUID).Scan(&id, &createdAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 回傳格式不變，前端可用
	c.JSON(http.StatusCreated, Task{
		ID: id, Title: in.Title, Description: in.Description, Category: in.Category,
		LocationText: in.LocationText, EstimatedMinutes: in.EstimatedMinutes,
		PrepayAmountCents: in.PrepayAmountCents, IsImmediate: in.IsImmediate,
		ScheduledAt: when, Requester: meEmail, RequesterID: meUID,
		Status: "open", CreatedAt: createdAt, AssignedTo: "", AssignedToID: nil,
	})
}

func scanTask(rows interface{ Scan(dest ...any) error }) (Task, error) {
	var t Task

	// 先用 NullString 接可能為 NULL 的 uuid 欄位
	var reqIDNS, assigneeIDNS sql.NullString

	err := rows.Scan(
		&t.ID, &t.Title, &t.Description, &t.Category, &t.LocationText,
		&t.EstimatedMinutes, &t.PrepayAmountCents, &t.IsImmediate,
		&t.ScheduledAt,
		&t.Requester, &reqIDNS, // requester_id -> NullString
		&t.Status, &t.CreatedAt,
		&t.AssignedTo, &assigneeIDNS, // assigned_to_id -> NullString
	)
	if err != nil {
		return t, err
	}

	// 將 NullString 映射回你的輸出型別
	t.RequesterID = reqIDNS.String // 若為 NULL 會是 ""
	if assigneeIDNS.Valid {
		id := assigneeIDNS.String
		t.AssignedToID = &id
	} else {
		t.AssignedToID = nil
	}
	return t, nil
}

func listMyTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status,created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where requester_id = $1::uuid
    order by created_at desc
  `, meUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
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
    select
      id, title, description, category, location_text,
      estimated_minutes, prepay_amount_cents, is_immediate,
      scheduled_at,
      requester, requester_id,
      status, created_at,
      assigned_to, assigned_to_id
    from public.tasks
    where id = $1
  `, id)

	t, err := scanTask(row)
	if err != nil {
		// 正確分辨「真的沒資料」與其他錯
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		} else {
			log.Printf("[getTask][ERROR] id=%s scan error: %v", id, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
		}
		return
	}
	c.JSON(http.StatusOK, t)
}

func updateTask(c *gin.Context) {
	id := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// 檢查擁有者 & 狀態
	// 檢查擁有者 & 狀態（用 requester_id）
	var requesterID, status string
	if err := db.QueryRow(ctx,
		`select requester_id, status from public.tasks where id=$1`, id,
	).Scan(&requesterID, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requesterID != meUID {
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
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status,created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where status='open' and requester_id <> $1::uuid and assigned_to_id is null
    order by created_at desc
  `, meUID)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listAssignedTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status,created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where assigned_to_id = $1::uuid and status='open'
    order by created_at desc
  `, meUID)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listDoneTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status,created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where assigned_to_id = $1::uuid and status='completed'
    order by created_at desc
  `, meUID)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func listMyPostedClosed(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	rows, err := db.Query(ctx, `
    select
      id, title, description, category, location_text,
      estimated_minutes, prepay_amount_cents, is_immediate,
      scheduled_at,
      requester, requester_id,
      status, created_at,
      assigned_to, assigned_to_id
    from public.tasks
    where requester_id = $1::uuid
      and status in ('completed','cancelled')
    order by created_at desc
  `, meUID)
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
	meUID := c.GetString("uid")
	meEmail := c.GetString("email")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()
	var requesterID, status string
	var assignedToID *string
	err := db.QueryRow(ctx, `
    select requester_id, status, assigned_to_id
    from public.tasks where id=$1
  `, id).Scan(&requesterID, &status, &assignedToID)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	if requesterID == meUID {
		c.JSON(400, gin.H{"error": "cannot accept your own task"})
		return
	}
	if status != "open" || assignedToID != nil {
		c.JSON(400, gin.H{"error": "not available"})
		return
	}

	_, err = db.Exec(ctx, `
    update public.tasks
    set assigned_to_id = $1::uuid,
        assigned_to    = $2
    where id = $3
  `, meUID, meEmail, id)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}

	getTask(c)
}

// -------- WorkLog handlers --------
const centsPerMinute = 50 // 0.5 EUR/min

func clockIn(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	meEmail := c.GetString("email")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// 用 uuid 判斷是否為受派者
	var status string
	var assignedToID sql.NullString
	if err := db.QueryRow(ctx, `
      select assigned_to_id, status
      from public.tasks
      where id=$1
    `, taskID).Scan(&assignedToID, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !assignedToID.Valid || assignedToID.String != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "only assignee can clock in"})
		return
	}
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task not open"})
		return
	}

	// 有沒有未結束的打卡（沿用 email 欄位）
	var exists bool
	_ = db.QueryRow(ctx, `
      select exists (
        select 1 from public.worklogs
        where task_id=$1 and "user"=$2 and end_at is null
      )`, taskID, meEmail).Scan(&exists)
	if exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already clocked in"})
		return
	}

	// 通知 requester
	notifyRequester(c, taskID, "CLOCK_IN",
		"Supporter clocked in",
		"The job has started. You can track progress in your dashboard.",
	)

	var id string
	var createdAt, startAt time.Time
	err := db.QueryRow(ctx, `
      insert into public.worklogs(task_id,"user",start_at)
      values ($1,$2,now())
      returning id, created_at, start_at
    `, taskID, meEmail).Scan(&id, &createdAt, &startAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusCreated, WorkLog{
		ID: id, TaskID: taskID, User: meEmail, Start: startAt, End: nil,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

func clockOut(c *gin.Context) {
	taskID := c.Param("id")
	meEmail := c.GetString("email")
	if meEmail == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var wlID string
	err := db.QueryRow(ctx, `
      select id
      from public.worklogs
      where task_id=$1 and "user"=$2 and end_at is null
      order by start_at asc limit 1
    `, taskID, meEmail).Scan(&wlID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no active session"})
		return
	}

	var startAt, endAt, createdAt, updatedAt time.Time
	err = db.QueryRow(ctx, `
      update public.worklogs
      set end_at=now(), updated_at=now()
      where id=$1
      returning start_at, end_at, created_at, updated_at
    `, wlID).Scan(&startAt, &endAt, &createdAt, &updatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, WorkLog{
		ID: wlID, TaskID: taskID, User: meEmail,
		Start: startAt, End: &endAt, CreatedAt: createdAt, UpdatedAt: updatedAt,
	})

	notifyRequester(c, taskID, "CLOCK_OUT",
		"Supporter clocked out",
		"Work session ended. We'll compute the bill and show the breakdown.",
	)
}

func getWorklogs(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var reqID, assID sql.NullString
	if err := db.QueryRow(ctx, `
      select requester_id, assigned_to_id
      from public.tasks where id=$1
    `, taskID).Scan(&reqID, &assID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if (!reqID.Valid || reqID.String != meUID) && (!assID.Valid || assID.String != meUID) {
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

	var totalMin int
	_ = db.QueryRow(ctx, `
      with x as (
        select ceil(extract(epoch from (end_at - start_at))/60.0) as m
        from public.worklogs
        where task_id=$1 and end_at is not null and end_at > start_at
      )
      select coalesce(sum(greatest(m,1))::int, 0) from x
    `, taskID).Scan(&totalMin)

	var hasOpen bool
	_ = db.QueryRow(ctx, `
      select exists (
        select 1 from public.worklogs where task_id=$1 and end_at is null
      )`, taskID).Scan(&hasOpen)

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
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var status, assigneeEmail string
	var requesterID, assignedToID sql.NullString
	if err := db.QueryRow(ctx, `
      select requester_id, assigned_to_id, assigned_to, status
      from public.tasks where id=$1
    `, taskID).Scan(&requesterID, &assignedToID, &assigneeEmail, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// 只有作者或受派者可以完成
	if (!requesterID.Valid || requesterID.String != meUID) &&
		(!assignedToID.Valid || assignedToID.String != meUID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already closed"})
		return
	}
	if !assignedToID.Valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "assignment required before completing"})
		return
	}

	var hasOpen bool
	_ = db.QueryRow(ctx, `
      select exists (
        select 1 from public.worklogs where task_id=$1 and end_at is null
      )`, taskID).Scan(&hasOpen)
	if hasOpen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clock-out required before completing"})
		return
	}

	// assignee 是否至少完成過一段工時（沿用 email）
	var hasClosedByAssignee bool
	_ = db.QueryRow(ctx, `
      select exists (
        select 1 from public.worklogs
        where task_id=$1 and "user"=$2 and end_at is not null
      )`, taskID, assigneeEmail).Scan(&hasClosedByAssignee)
	if !hasClosedByAssignee {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one work session is required before completing"})
		return
	}

	notifyRequester(c, taskID, "COMPLETED",
		"Job completed",
		"Everything is done. Please leave a rating when you have a moment.",
	)

	if _, err := db.Exec(ctx, `update public.tasks set status='completed' where id=$1`, taskID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	getTask(c)
}

// -------- Notifications --------
func OnOrderAccepted(ctx context.Context, db *sql.DB, jobID, requesterID, supporterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, JobID: jobID, Type: "ORDER_ACCEPTED",
		Title:     "Your request was accepted",
		Body:      "Your supporter has accepted the job. You'll be notified when they clock in.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

// 打卡開始
func OnClockIn(ctx context.Context, db *sql.DB, jobID, requesterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, JobID: jobID, Type: "CLOCK_IN",
		Title:     "Supporter clocked in",
		Body:      "The job has started. You can track progress in your dashboard.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

// 打卡結束
func OnClockOut(ctx context.Context, db *sql.DB, jobID, requesterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, JobID: jobID, Type: "CLOCK_OUT",
		Title:     "Supporter clocked out",
		Body:      "Work session ended. We'll compute the bill and show the breakdown.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

// 完成
func OnCompleted(ctx context.Context, db *sql.DB, jobID, requesterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, JobID: jobID, Type: "COMPLETED",
		Title:     "Job completed",
		Body:      "Everything is done. Please leave a rating when you have a moment.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

// 取消（同時寫 audit_log）
func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func CancelJob(ctx context.Context, db *sql.DB, jobID, actorID string, reason string) error {
	// 1) 檢查 open worklog
	var openCnt int
	if err := db.QueryRowContext(ctx, `
    SELECT count(*) FROM worklogs WHERE task_id=$1 AND clock_in_at IS NOT NULL AND clock_out_at IS NULL
  `, jobID).Scan(&openCnt); err != nil {
		return err
	}
	if openCnt > 0 {
		return fmt.Errorf("cannot cancel: open worklog exists")
	}

	// 2) 計算金額（略：依你現有 schema）
	summary := map[string]any{"refund": 12.34, "billableHours": 1.25}

	// 3) 寫 audit_logs
	_, err := db.ExecContext(ctx, `
    INSERT INTO audit_logs (task_id, actor_id, action, reason, meta)
    VALUES ($1,$2,'JOB_CANCELLED',$3,$4::jsonb)
  `, jobID, actorID, reason, toJSON(summary))
	if err != nil {
		return err
	}

	// 4) 通知雙方（這裡示例只給 requester）
	requesterID, requesterEmail := "...", "..."
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, JobID: jobID, Type: "CANCELLED",
		Title:     "Task cancelled",
		Body:      fmt.Sprintf("The job was cancelled. Reason: %s", reason),
		SendEmail: true, EmailTo: requesterEmail,
	})
	return nil
}

func RegisterNotificationRoutes(g *gin.Engine, db *sql.DB) {
	grp := g.Group("/")
	grp.Use(authMiddleware())

	// GET /notifications
	grp.GET("/notifications", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}

		unread := c.Query("unread") == "true"

		limit := 50
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if n > 200 {
					n = 200
				}
				limit = n
			}
		}

		var before *time.Time
		if s := c.Query("before"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				before = &t
			}
		}

		q := `
		SELECT id, task_id, type, title, body, unread, via_email, email_sent_at, created_at
		FROM public.notifications
		WHERE user_id = $1::uuid
	`
		args := []any{me}
		arg := 2
		if unread {
			q += " AND unread = true"
		}
		if before != nil {
			q += fmt.Sprintf(" AND created_at < $%d", arg)
			args = append(args, *before)
			arg++
		}
		q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", arg)
		args = append(args, limit)

		log.Printf("[notifications][GET] uid=%s sql=%s args=%v", me, q, args) // 👈 印 SQL 與參數

		rows, err := db.QueryContext(c.Request.Context(), q, args...)
		if err != nil {
			log.Printf("[notifications][GET][query] err=%v", err) // 👈 印真正錯誤
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()

		out := make([]NotificationDTO, 0, limit)
		for rows.Next() {
			var n NotificationDTO
			if err := rows.Scan(
				&n.ID, &n.TaskID, &n.Type, &n.Title, &n.Body,
				&n.Unread, &n.ViaEmail, &n.EmailSentAt, &n.CreatedAt,
			); err != nil {
				log.Printf("[notifications][GET][scan] err=%v", err) // 👈 最常見：欄位不存在/型別不合
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, n)
		}
		c.JSON(http.StatusOK, out)
	})

	// 單筆設為已讀
	grp.PATCH("/notifications/:id/read", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		id := c.Param("id")
		_, err := db.ExecContext(c.Request.Context(),
			`UPDATE public.notifications
          SET unread = false
        WHERE id = $1::uuid AND user_id = $2::uuid`, // ← 修掉 AND AND，兩個都 ::uuid
			id, me,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	// 全部設為已讀
	grp.POST("/notifications/mark-read-all", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		_, err := db.ExecContext(c.Request.Context(),
			`UPDATE public.notifications
          SET unread = false
        WHERE user_id = $1::uuid AND unread = true`,
			me,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
}

// 由 taskID 取得 requester 的 uid（uuid）與 email
func requesterUIDAndEmail(ctx context.Context, taskID string) (uid string, email string, err error) {
	err = sqldb.QueryRowContext(ctx, `
		SELECT u.id::text, t.requester
		FROM public.tasks t
		JOIN auth.users u ON lower(u.email) = lower(t.requester)
		WHERE t.id = $1
	`, taskID).Scan(&uid, &email)
	return
}

// 共用通知 helper：用 uid 寫入 notifications，用 email 寄信
func notifyRequester(c *gin.Context, taskID, ntype, title, body string) {
	ctx := c.Request.Context()

	uid, email, err := requesterUIDAndEmail(ctx, taskID)
	if err != nil {
		log.Printf("[notify][skip] lookup uid/email: %v", err)
		return
	}
	log.Printf("[hook] %s job=%s requester_uid=%s email=%s", ntype, taskID, uid, email)

	// 寫站內通知 +（可選）寄信
	if err := notify.Create(ctx, notify.CreateNotificationInput{
		DB:        sqldb, // ← 用 *sql.DB（pgx stdlib）
		UserID:    uid,   // ← 這裡塞「uuid」
		JobID:     taskID,
		Type:      ntype,
		Title:     title,
		Body:      body,
		SendEmail: email != "",
		EmailTo:   email,
	}); err != nil {
		log.Printf("[notify][ERROR] %s: %v", ntype, err)
	}
}
