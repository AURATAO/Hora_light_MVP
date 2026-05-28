// Server entrypoint for Hora (Version A: backend ↔ Supabase Postgres).
// - Auth: validates Supabase JWT via JWKS (RS256).
// - DB: connects directly to Supabase Postgres using pgxpool.
// - Scope: profiles, tasks, worklogs CRUD and basic business flows.
// Required ENV:
//   SUPABASE_PROJECT_URL=https://<ref>.supabase.co
//   SUPABASE_DB_URL=postgres://postgres:<password>@db.<ref>.supabase.co:5432/postgres?sslmode=require

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	notify "hora-auth/internal/notify"
	"os"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"hora-auth/auth"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	opsroutes "hora-auth/ops"
	"hora-auth/helpers"

	storage_go "github.com/supabase-community/storage-go"
)

// Fetch JWKS from Supabase and auto-refresh. This validates access tokens
// issued by your project (RS256). If the issuer doesn't match, we reject.

var jwks *keyfunc.JWKS
var db *pgxpool.Pool
var sqldb *sql.DB

var DB *sql.DB

func SetDB(db *sql.DB) { DB = db }

var opsAdmins = map[string]struct{}{
	"auratao.model@gmail.com": {},
	"liang.you@horaapp.co":    {},
	"liang.you@arcodiax.com":  {},
	"rollod4@gmail.com":       {},
	"daniele@arcodiax.com":    {},
}

func isOpsAdminEmail(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	_, ok := opsAdmins[s]
	return ok
}

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

	// uuid（後端查詢/權限全靠它）
	RequesterID  string  `json:"requester_id"`
	AssignedToID *string `json:"assigned_to_id,omitempty"`

	// Travel time estimate (populated after supporter accepts)
	TravelTimeMinutes    *int `json:"travel_time_minutes,omitempty"`
	TotalEstimateMinutes *int `json:"total_estimate_minutes,omitempty"`
}

type createTaskInput struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	Category           string `json:"category"`
	LocationText       string `json:"location_text"`
	EstimatedMinutes   int    `json:"estimated_minutes"`
	PrepayAmountCents  int    `json:"prepay_amount_cents"`
	IsImmediate        bool   `json:"is_immediate"`
	ScheduledAt        string `json:"scheduled_at"` // ISO8601 (RFC3339) 或空字串
	TransportRequired  string `json:"transport_required"`
}

type Profile struct {
	Email               string     `json:"email"`
	Name                string     `json:"name"`
	Phone               string     `json:"phone"`
	City                string     `json:"city"`
	AvatarURL           string     `json:"avatar_url"`
	Bio                 string     `json:"bio"`
	BetaAccepted        bool       `json:"beta_accepted"`
	IsVerifiedSupporter bool       `json:"is_verified_supporter"`
	SupporterAppliedAt  *time.Time `json:"supporter_applied_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type PublicProfile struct {
	// ProfilePublic is a safe view for public profile
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	City      string    `json:"city"`
	Phone     string    `json:"phone"`
	AvatarURL string    `json:"avatar_url"`
	Bio       string    `json:"bio"`
	CreatedAt time.Time `json:"created_at"`

	// stats
	PostedTotal     int `json:"posted_total"`
	PostedCompleted int `json:"posted_completed"`
	AsgInProgress   int `json:"asg_in_progress"`
	AsgCompleted    int `json:"asg_completed"`
}

type TaskListItem struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Category         string    `json:"category"`
	EstimatedMinutes int       `json:"estimated_minutes"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	RequesterID      string    `json:"requester_id"`
	AssignedToID     *string   `json:"assigned_to_id,omitempty"`
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

	// --- JWKS (Supabase Bearer 驗證用) ---
	projectURL := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
	jwksURL := strings.TrimSpace(os.Getenv("SUPABASE_JWKS_URL"))
	if jwksURL == "" && projectURL != "" {
		jwksURL = projectURL + "/auth/v1/keys"
	}
	if jwksURL == "" {
		log.Fatal("SUPABASE_JWKS_URL is not set (hint: set SUPABASE_PROJECT_URL=https://<ref>.supabase.co)")
	}
	var err error
	jwks, err = keyfunc.Get(jwksURL, keyfunc.Options{
		RefreshInterval: time.Hour,
		RefreshTimeout:  10 * time.Second,
		Ctx:             context.Background(),
		RefreshErrorHandler: func(err error) {
			log.Printf("[jwks] refresh error: %v", err)
		},
	})
	if err != nil {
		log.Fatalf("failed to init JWKS: %v", err)
	}
	log.Printf("[auth] using JWKS URL: %s", jwksURL)

	// --- DB 連線（pgxpool + database/sql）---
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

	pgxCfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("pgx ParseConfig (stdlib) error: %v", err)
	}
	pgxCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pgxCfg.StatementCacheCapacity = 0
	dsn := stdlib.RegisterConnConfig(pgxCfg)

	sqldb, err = sql.Open("pgx", dsn)
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

	// Email diagnostics
	postmarkSet := os.Getenv("POSTMARK_API_TOKEN") != ""
	log.Printf("[email] POSTMARK_API_TOKEN set: %v", postmarkSet)
	if !postmarkSet {
		log.Println("[email] WARNING: POSTMARK_API_TOKEN is empty — emails will not be sent")
	}

	// --- Google OAuth 初始化 & 注入 DB（只做一次！）---
	if err := auth.InitGoogleOAuth(); err != nil {
		log.Fatalf("InitGoogleOAuth error: %v", err)
	}
	auth.SetDB(sqldb)
	log.Println("[auth] DB injected")

	// --- Gin & CORS ---
	r := gin.Default()
	r.SetTrustedProxies(nil)
	origins := os.Getenv("CORS_ALLOW_ORIGINS") // comma-separated exact origins
	exactOrigins := strings.Split(origins, ",")
	c := cors.Config{
		AllowOriginFunc: func(origin string) bool {
			// always allow exact matches from env
			for _, o := range exactOrigins {
				if strings.TrimSpace(o) == origin {
					return true
				}
			}
			// allow all Vercel preview deployments for this project
			if strings.HasSuffix(origin, ".vercel.app") {
				return true
			}
			return false
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}
	r.Use(cors.New(c))
	r.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// --- 路由 ---
	RegisterWhatsAppWebhooks(r)
	RegisterNotificationRoutes(r, sqldb)

	addAvatarUploadRouteV1(r)

	opsroutes.RegisterOpsRoutes(r, sqldb, dualAuth(sqldb), func(email string) bool {
		return isOpsAdminEmail(email)
	})

	// 列出所有路由（除錯用）

	r.GET("/__routes", func(c *gin.Context) {
		type R struct{ Method, Path string }
		rs := []R{}
		for _, rt := range r.Routes() {
			rs = append(rs, R{Method: rt.Method, Path: rt.Path})
		}
		c.JSON(200, rs)
	})

	// 公開個人頁（你要「登入可看更完整，未登入也可看」→ 用 tryAuth）
	profilesAPI := r.Group("/profiles")
	profilesAPI.Use(tryAuth(sqldb))
	{
		profilesAPI.GET("/:id", getProfileByID)
		profilesAPI.GET("/:id/tasks", listProfileTasks)
		profilesAPI.GET("/:id/reviews", listProfileReviews)
	}

	// Google OAuth
	r.GET("/auth/login", auth.HandleLogin)
	r.GET("/auth/callback", auth.HandleCallback)

	// /auth/me（不噴 401）
	r.GET("/auth/me", tryAuth(sqldb), func(c *gin.Context) {
		uid := c.GetString("uid")
		email := c.GetString("email")
		if uid == "" {
			c.JSON(http.StatusOK, gin.H{"auth": false})
			return
		}
		var name string
		var isVerified bool
		_ = db.QueryRow(c.Request.Context(),
			`select coalesce(name,''), coalesce(is_verified_supporter, false) from public.profiles where email = $1`,
			email,
		).Scan(&name, &isVerified)
		if name == "" {
			name = deriveName(email)
		}
		c.JSON(http.StatusOK, gin.H{
			"auth":                 true,
			"id":                   uid,
			"email":                email,
			"name":                 name,
			"is_verified_supporter": isVerified,
		})
	})

	// 根路徑 → 你的前端（/app/）
	r.GET("/", func(c *gin.Context) {
		base := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
		if base == "" {
			base = "http://localhost:5173/app"
		}
		c.Redirect(http.StatusFound, strings.TrimRight(base, "/")+"/")
	})

	r.POST("/auth/logout", func(c *gin.Context) {
		isProd := strings.EqualFold(os.Getenv("APP_ENV"), "prod") || strings.EqualFold(os.Getenv("COOKIE_SECURE"), "true")

		cookie := &http.Cookie{
			Name:     "hora_session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   isProd, // ✅ prod 要 true
		}
		if isProd {
			cookie.SameSite = http.SameSiteNoneMode // ✅ 跨網域需要
		} else {
			cookie.SameSite = http.SameSiteLaxMode
		}
		http.SetCookie(c.Writer, cookie)
		c.Status(http.StatusNoContent)
	})

	// POST /auth/exchange — swap a Supabase access token for an internal hora_session cookie.
	// Called after magic-link / OTP verification so the user gets the same cookie as Google OAuth.
	r.POST("/auth/exchange", func(c *gin.Context) {
		var body struct {
			AccessToken string `json:"access_token"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.AccessToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing access_token"})
			return
		}

		// Validate Supabase JWT — support both HS256 (default) and RS256 (JWKS).
		// Supabase signs access tokens with HS256 using the JWT secret as raw bytes.
		tok, err := jwt.Parse(body.AccessToken, func(t *jwt.Token) (interface{}, error) {
			switch t.Method.Alg() {
			case "HS256":
				return []byte(os.Getenv("SUPABASE_JWT_SECRET")), nil
			default:
				return jwks.Keyfunc(t)
			}
		})
		if err != nil || !tok.Valid {
			log.Printf("[exchange] token validation failed: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		claims, ok := tok.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "bad claims"})
			return
		}
		extSub, _ := claims["sub"].(string)
		email, _ := claims["email"].(string)
		if extSub == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing sub"})
			return
		}

		// Upsert user in public.users — with email-based account linking.
		// Priority: supabase_sub match → email match (link accounts) → new row.
		var internalID string
		err = sqldb.QueryRowContext(c.Request.Context(),
			`select id from public.users where supabase_sub = $1::uuid`, extSub,
		).Scan(&internalID)
		if errors.Is(err, sql.ErrNoRows) && email != "" {
			// Try to find existing account by email (e.g. user already signed in via Google)
			err = sqldb.QueryRowContext(c.Request.Context(),
				`update public.users set supabase_sub = $1::uuid where email = $2 returning id`,
				extSub, email,
			).Scan(&internalID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			// Truly new user — create a fresh row
			name := deriveName(email)
			err = sqldb.QueryRowContext(c.Request.Context(), `
				insert into public.users (supabase_sub, email, name)
				values ($1::uuid, $2, $3)
				returning id
			`, extSub, email, name).Scan(&internalID)
		}
		if err != nil || internalID == "" {
			log.Printf("[exchange] upsert user err=%v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "user upsert failed"})
			return
		}

		// Issue internal hora_session cookie (same as Google OAuth path)
		isProd := strings.EqualFold(os.Getenv("APP_ENV"), "prod") || strings.EqualFold(os.Getenv("COOKIE_SECURE"), "true")
		ttl := 24 * time.Hour
		now := time.Now()
		j := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
			Email: email,
			Name:  deriveName(email),
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   internalID,
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			},
		})
		signed, err := j.SignedString([]byte(os.Getenv("SESSION_JWT_SECRET")))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "session error"})
			return
		}
		cookie := &http.Cookie{
			Name:     "hora_session",
			Value:    signed,
			Path:     "/",
			HttpOnly: true,
			Secure:   isProd,
			MaxAge:   int(ttl.Seconds()),
		}
		if isProd {
			cookie.SameSite = http.SameSiteNoneMode
		} else {
			cookie.SameSite = http.SameSiteLaxMode
		}
		http.SetCookie(c.Writer, cookie)
		c.JSON(http.StatusOK, gin.H{"auth": true, "id": internalID, "email": email, "name": deriveName(email)})
	})

	// 需要登入的 API
	meAPI := r.Group("/profile")
	meAPI.Use(dualAuth(sqldb))
	{
		meAPI.GET("", getMyProfile)
		meAPI.PATCH("", patchMyProfile)
	}

	tasksAPI := r.Group("/tasks")
	tasksAPI.Use(dualAuth(sqldb))
	{
		tasksAPI.POST("", createTask)
		tasksAPI.GET("", listMyTasks)
		tasksAPI.GET("/:id", getTask)
		tasksAPI.PATCH("/:id", updateTask)

		tasksAPI.GET("/available", listAvailableTasks)
		tasksAPI.GET("/assigned", listAssignedTasks)
		tasksAPI.GET("/posted", listMyTasks)
		tasksAPI.GET("/done", listDoneTasks)
		tasksAPI.GET("/posted/closed", listMyPostedClosed)

		tasksAPI.POST("/:id/accept", acceptTask)
		tasksAPI.POST("/:id/complete", completeTask)
		tasksAPI.POST("/:id/completion-photo", uploadTaskCompletionPhoto)
		tasksAPI.POST("/:id/cancel", cancelTask)

		tasksAPI.POST("/:id/clock-in", clockIn)
		tasksAPI.POST("/:id/clock-out", clockOut)
		tasksAPI.GET("/:id/worklogs", getWorklogs)

		tasksAPI.POST("/:id/gps-ping", saveGpsPing)
		tasksAPI.GET("/:id/gps-latest", getLatestGps)
		tasksAPI.POST("/:id/estimate-travel", estimateTravel)
		tasksAPI.POST("/:id/review", createReview)
	}

	// dev-only WhatsApp test route
	if os.Getenv("GO_ENV") == "development" {
		r.GET("/test-whatsapp", func(c *gin.Context) {
			to := os.Getenv("TEST_WHATSAPP_NUMBER")
			if err := helpers.SendWhatsAppMessage(to, "Hi! This is Hora. WhatsApp integration is working!"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"success": true})
		})
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func parseLimit(q string, def, max int) int {
	if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= max {
		return n
	}
	return def
}

// func me(c *gin.Context) {
// 	uid := c.GetString("uid")
// 	email := c.GetString("email")
// 	if uid == "" {
// 		c.JSON(http.StatusOK, gin.H{"auth": false})
// 		return
// 	}
// 	c.JSON(http.StatusOK, gin.H{
// 		"auth":  true,
// 		"id":    uid,
// 		"email": email,
// 		"name":  deriveName(email),
// 	})
// }

// Verify "Bearer <JWT>" using JWKS and enforce issuer = <PROJECT_URL>/auth/v1.
// Exposes: c.Set("uid") = sub (Supabase user UUID), c.Set("email") if present.

// func authMiddleware() gin.HandlerFunc {
// 	return func(c *gin.Context) {
// 		authz := c.GetHeader("Authorization")
// 		if !strings.HasPrefix(authz, "Bearer ") {
// 			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
// 			return
// 		}
// 		tokenStr := strings.TrimPrefix(authz, "Bearer ")

// 		// 依 alg 選擇驗證方式
// 		keyfunc := func(t *jwt.Token) (interface{}, error) {
// 			alg := t.Method.Alg()
// 			switch alg {
// 			case "HS256", "HS384", "HS512":
// 				secret := strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET"))
// 				if secret == "" {
// 					return nil, fmt.Errorf("SUPABASE_JWT_SECRET is not set")
// 				}
// 				return []byte(secret), nil
// 			case "RS256", "RS384", "RS512":
// 				if jwks == nil {
// 					return nil, fmt.Errorf("jwks not initialized")
// 				}
// 				return jwks.Keyfunc(t)
// 			default:
// 				return nil, fmt.Errorf("unsupported alg: %s", alg)
// 			}
// 		}

// 		token, err := jwt.Parse(tokenStr, keyfunc)
// 		if err != nil || !token.Valid {
// 			log.Printf("[auth] invalid token: %v", err)
// 			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
// 			return
// 		}
// 		claims, ok := token.Claims.(jwt.MapClaims)
// 		if !ok {
// 			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid claims"})
// 			return
// 		}

// 		// Issuer 檢查
// 		expIss := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/") + "/auth/v1"
// 		if iss := fmt.Sprint(claims["iss"]); expIss != "" && iss != expIss {
// 			log.Printf("[auth] invalid issuer: got=%s want=%s", iss, expIss)
// 			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid issuer"})
// 			return
// 		}

// 		c.Set("claims", claims)
// 		c.Set("uid", fmt.Sprint(claims["sub"]))
// 		if email, _ := claims["email"].(string); email != "" {
// 			c.Set("email", email)
// 		}
// 		c.Next()
// 	}
// }

// -------- Auth handlers (OTP via email) --------
// deriveName: naive display name from email local-part; replace with real profile later.
// e.g. "jane.doe@x.com" -> "Jane Doe"

func deriveName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return cases.Title(language.Und).String(strings.ReplaceAll(email[:i], ".", " "))
	}
	return email
}

// -------- Profile handlers --------
// getMyProfile: lazy-create profile if missing (idempotent).
// patchMyProfile: upsert via ON CONFLICT(email).

func mustEnv(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		log.Fatalf("missing required env: %s", name)
	}
	return v
}

func newStorageClientV1() *storage_go.Client {
	base := strings.TrimSuffix(mustEnv("SUPABASE_PROJECT_URL"), "/")
	key := mustEnv("SUPABASE_SERVICE_ROLE_KEY") // 務必是 service_role 的 key
	log.Printf("[storage] endpoint=%s/storage/v1", base)
	return storage_go.NewClient(base+"/storage/v1", key, nil)
}

func addAvatarUploadRouteV1(r *gin.Engine) {
	log.Println("[BOOT] addAvatarUploadRouteV1 ENTER")

	st := newStorageClientV1()
	bucket := strings.TrimSpace(os.Getenv("AVATARS_BUCKET"))
	if bucket == "" {
		bucket = "avatars"
	}
	log.Printf("[storage] bucket=%s", bucket)

	isProd := strings.EqualFold(os.Getenv("APP_ENV"), "prod")

	fail := func(c *gin.Context, code int, msg string, err error) {
		if err != nil {
			log.Printf("[avatar][ERR] %s: %v", msg, err)
		} else {
			log.Printf("[avatar][ERR] %s", msg)
		}
		payload := gin.H{"error": msg}
		if !isProd && err != nil {
			payload["detail"] = err.Error()
		}
		c.JSON(code, payload)
	}

	r.POST("/profile/avatar", dualAuth(sqldb), func(c *gin.Context) {
		uid := c.GetString("uid")
		if uid == "" {
			fail(c, http.StatusUnauthorized, "unauthenticated", nil)
			return
		}

		// 限 5MB
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 5<<20)

		// 讀檔（必須是 multipart/form-data，欄位名 "file"）
		f, hdr, err := c.Request.FormFile("file")
		if err != nil {
			fail(c, http.StatusBadRequest, "file required (multipart/form-data; field name 'file')", err)
			return
		}
		defer f.Close()

		// content-type（必要時用副檔名推）
		contentType := hdr.Header.Get("Content-Type")
		if contentType == "" {
			switch strings.ToLower(filepath.Ext(hdr.Filename)) {
			case ".jpg", ".jpeg":
				contentType = "image/jpeg"
			case ".png":
				contentType = "image/png"
			case ".webp":
				contentType = "image/webp"
			case ".gif":
				contentType = "image/gif"
			case ".heic":
				contentType = "image/heic"
			case ".heif":
				contentType = "image/heif"
			default:
				contentType = "application/octet-stream"
			}
		}

		// 讀進記憶體
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, f); err != nil {
			fail(c, http.StatusInternalServerError, "read file failed", err)
			return
		}

		// 目標 key
		ext := strings.ToLower(filepath.Ext(hdr.Filename))
		if ext == "" {
			ext = ".jpg"
		}
		key := fmt.Sprintf("%s/avatar-%d%s", uid, time.Now().Unix(), ext)
		log.Printf("[avatar] uid=%s key=%s ct=%s size=%d", uid, key, contentType, buf.Len())

		// 1) 取簽名上傳 URL
		signed, err := st.CreateSignedUploadUrl(bucket, key)
		if err != nil || signed.Url == "" {
			log.Printf("[avatar][signed-url][ERR] resp=%+v err=%v", signed, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "signed url failed"})
			return
		}

		// 1.5) 轉成絕對網址（你的 SUPABASE_PROJECT_URL 例如 https://xxxx.supabase.co）
		base := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
		uploadURL := signed.Url
		switch {
		case strings.HasPrefix(uploadURL, "http://") || strings.HasPrefix(uploadURL, "https://"):
			// 已是完整網址，OK
		case strings.HasPrefix(uploadURL, "/"):
			// 相對於 /storage/v1
			uploadURL = base + "/storage/v1" + uploadURL
		default:
			// 沒有斜線、但也不是 http(s) → 當作相對於 /storage/v1
			uploadURL = base + "/storage/v1/" + uploadURL
		}
		log.Printf("[avatar] uploadURL=%s", uploadURL)

		// 2) PUT 上傳（顯式帶 Content-Type、x-upsert）
		req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(buf.Bytes()))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "build request failed"})
			return
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("x-upsert", "true")
		req.Header.Set("Cache-Control", "public, max-age=3600")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[avatar][upload-signed][ERR] %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "upload failed"})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			log.Printf("[avatar][upload-signed][HTTP %d] %s", resp.StatusCode, string(b))
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("upload failed (%d)", resp.StatusCode)})
			return
		}
		log.Printf("[avatar][upload-signed][OK] ct=%s size=%d", contentType, buf.Len())

		// 3) 取公開網址
		pub := st.GetPublicUrl(bucket, key)
		publicURL := pub.SignedURL
		if publicURL == "" {
			base := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
			publicURL = fmt.Sprintf("%s/storage/v1/object/public/%s/%s", base, bucket, key)
			log.Printf("[avatar][publicURL][fallback] %s", publicURL)
		} else {
			log.Printf("[avatar][publicURL] %s", publicURL)
		}

		// 4) 存 DB
		if _, err := db.Exec(c.Request.Context(),
			`UPDATE public.profiles SET avatar_url=$1, updated_at=now() WHERE id=$2::uuid`,
			publicURL, uid,
		); err != nil {
			fail(c, http.StatusInternalServerError, "db error", err)
			return
		}

		// 5) 回傳
		c.JSON(http.StatusOK, gin.H{"url": publicURL})
	})

	log.Println("[http] POST /profile/avatar mounted")
}

func getMyProfile(c *gin.Context) {
	uid := c.GetString("uid")
	email := c.GetString("email")
	ctx := c.Request.Context()

	if uid == "" || email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var p Profile
	var existingID *string

	// 以 email 先找一筆（你本來就這樣）
	err := db.QueryRow(ctx, `
    select id::text, email, name, phone, city, avatar_url, bio, beta_accepted,
           coalesce(is_verified_supporter, false), supporter_applied_at,
           created_at, updated_at
    from public.profiles
    where email = $1
  `, email).Scan(&existingID, &p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.BetaAccepted, &p.IsVerifiedSupporter, &p.SupporterAppliedAt, &p.CreatedAt, &p.UpdatedAt)

	now := time.Now()

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 沒有 → 直接用 uid 當 id 建
		_, err2 := db.Exec(ctx, `
      insert into public.profiles(id,email,name,phone,city,avatar_url,bio,created_at,updated_at)
      values ($1::uuid,$2,$3,'','','','',$4,$4)
    `, uid, email, deriveName(email), now)
		if err2 != nil {
			log.Printf("[profile][insert uid] email=%s err=%v", email, err2)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		p = Profile{
			Email: email, Name: deriveName(email),
			CreatedAt: now, UpdatedAt: now,
		}

	case err == nil:
		// 有 → 若 id 舊的不是 uid，矯正為 uid（一次性修復）
		if existingID != nil && *existingID != uid {
			_, errFix := db.Exec(ctx, `
        update public.profiles
        set id = $1::uuid, updated_at = $2
        where email = $3
      `, uid, now, email)
			if errFix != nil {
				log.Printf("[profile][fix id] email=%s from=%s to=%s err=%v", email, *existingID, uid, errFix)
			}
		}

	default:
		log.Printf("[profile][select] email=%s err=%v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 回傳最新資料
	_ = db.QueryRow(ctx, `
    select email, name, phone, city, avatar_url, bio, beta_accepted,
           coalesce(is_verified_supporter, false), supporter_applied_at,
           created_at, updated_at
    from public.profiles where email = $1
  `, email).Scan(&p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.BetaAccepted, &p.IsVerifiedSupporter, &p.SupporterAppliedAt, &p.CreatedAt, &p.UpdatedAt)

	c.JSON(http.StatusOK, p)
}

func patchMyProfile(c *gin.Context) {
	uid := c.GetString("uid")
	email := c.GetString("email")
	if uid == "" || email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var in struct {
		Name         *string `json:"name"`
		Phone        *string `json:"phone"`
		City         *string `json:"city"`
		AvatarURL    *string `json:"avatar_url"`
		Bio          *string `json:"bio"`
		BetaAccepted *bool   `json:"beta_accepted"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	ctx := c.Request.Context()
	var p Profile
	_ = db.QueryRow(ctx, `
    select email, name, phone, city, avatar_url, bio, beta_accepted,
           coalesce(is_verified_supporter, false), supporter_applied_at,
           created_at, updated_at
    from public.profiles where email = $1
  `, email).Scan(&p.Email, &p.Name, &p.Phone, &p.City, &p.AvatarURL, &p.Bio, &p.BetaAccepted, &p.IsVerifiedSupporter, &p.SupporterAppliedAt, &p.CreatedAt, &p.UpdatedAt)

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
	if in.BetaAccepted != nil {
		p.BetaAccepted = *in.BetaAccepted
	}
	p.Email = email
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.UpdatedAt = time.Now()

	_, err := db.Exec(ctx, `
	insert into public.profiles(id,email,name,phone,city,avatar_url,bio,beta_accepted,created_at,updated_at)
	values ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	on conflict (email) do update
		set name=$3, phone=$4, city=$5, avatar_url=$6, bio=$7, beta_accepted=$8, updated_at=$10
	`, uid, p.Email, p.Name, p.Phone, p.City, p.AvatarURL, p.Bio, p.BetaAccepted, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		log.Printf("[profile][upsert] email=%s err=%v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func getProfileByID(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	var p PublicProfile
	err := db.QueryRow(ctx, `
    select id, name, city, phone, avatar_url, bio, created_at
    from public.profiles
    where id = $1::uuid
  `, id).Scan(&p.ID, &p.Name, &p.City, &p.Phone, &p.AvatarURL, &p.Bio, &p.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// Fallback: 先從 users 抓 email
		var email sql.NullString
		_ = db.QueryRow(ctx, `select email from public.users where id = $1::uuid`, id).Scan(&email)
		if !email.Valid {
			// 再從 tasks（有人用這個 id 發/接過單）
			_ = db.QueryRow(ctx, `
        select requester from public.tasks where requester_id = $1::uuid
        order by created_at desc limit 1
      `, id).Scan(&email)
			if !email.Valid {
				_ = db.QueryRow(ctx, `
          select assigned_to from public.tasks where assigned_to_id = $1::uuid
          order by created_at desc limit 1
        `, id).Scan(&email)
			}
		}
		if !email.Valid {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		// 直接回推導的 profile（也可順手 upsert 保存）
		name := deriveName(email.String)
		now := time.Now()
		p = PublicProfile{
			ID: id, Name: name, AvatarURL: "", City: "", Bio: "", CreatedAt: now,
		}

		// （可選）順手 upsert，避免下次再 fallback
		_, _ = db.Exec(ctx, `
      insert into public.profiles (id,email,name,created_at,updated_at)
      values ($1::uuid,$2,$3,$4,$4)
      on conflict (email) do update set
        name = coalesce(nullif($3,''), public.profiles.name),
        updated_at = greatest(public.profiles.updated_at, $4)
    `, id, email.String, name, now)

	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 統計
	_ = db.QueryRow(ctx, `select count(*) from public.tasks where requester_id=$1::uuid`, id).Scan(&p.PostedTotal)
	_ = db.QueryRow(ctx, `select count(*) from public.tasks where requester_id=$1::uuid and status='completed'`, id).Scan(&p.PostedCompleted)
	_ = db.QueryRow(ctx, `select count(*) from public.tasks where assigned_to_id=$1::uuid and status='open'`, id).Scan(&p.AsgInProgress)
	_ = db.QueryRow(ctx, `select count(*) from public.tasks where assigned_to_id=$1::uuid and status='completed'`, id).Scan(&p.AsgCompleted)

	c.JSON(http.StatusOK, p)
}

func listProfileTasks(c *gin.Context) {
	uid := c.Param("id")
	role := c.Query("role")     // "assignee" | "requester"
	status := c.Query("status") // "open" | "completed" | "all"
	limit := 20
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	var before *time.Time
	if s := c.Query("before"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			before = &t
		}
	}

	if role != "assignee" && role != "requester" {
		c.JSON(400, gin.H{"error": "invalid role"})
		return
	}
	if status == "" {
		status = "all"
	}

	where := []string{}
	args := []any{}
	arg := 1

	if role == "assignee" {
		where = append(where, fmt.Sprintf("assigned_to_id = $%d::uuid", arg))
	} else {
		where = append(where, fmt.Sprintf("requester_id = $%d::uuid", arg))
	}
	args = append(args, uid)
	arg++

	if status == "open" || status == "completed" {
		where = append(where, fmt.Sprintf("status = $%d", arg))
		args = append(args, status)
		arg++
	}
	if before != nil {
		where = append(where, fmt.Sprintf("created_at < $%d", arg))
		args = append(args, *before)
		arg++
	}

	q := fmt.Sprintf(`
    select id, title, category, estimated_minutes, status, created_at, requester_id, assigned_to_id
    from public.tasks
    where %s
    order by created_at desc
    limit $%d
  `, strings.Join(where, " AND "), arg)
	args = append(args, limit)

	rows, err := db.Query(c.Request.Context(), q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	out := []TaskListItem{}
	for rows.Next() {
		var it TaskListItem
		if err := rows.Scan(&it.ID, &it.Title, &it.Category, &it.EstimatedMinutes, &it.Status, &it.CreatedAt, &it.RequesterID, &it.AssignedToID); err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		out = append(out, it)
	}
	c.JSON(200, out)
}

// -------- Tasks handlers --------
func createTask(c *gin.Context) {
	var in createTaskInput
	if err := c.BindJSON(&in); err != nil {
		log.Printf("[createTask] BindJSON error: %v", err)
		c.JSON(400, gin.H{"error": "invalid payload"})
		return
	}
	// sanitize
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Category = strings.TrimSpace(in.Category)
	in.LocationText = strings.TrimSpace(in.LocationText)
	log.Printf("[createTask] body: %+v", in)
	if in.Title == "" {
		c.JSON(400, gin.H{"error": "title required"})
		return
	}
	if in.EstimatedMinutes <= 0 {
		in.EstimatedMinutes = 30
	}
	if in.Category == "" {
		in.Category = "quick_errand"
	}
	validCategories := map[string]bool{
		"task": true, "companion": true,
		"quick_errand": true, "standard": true, "half_day": true, "full_day": true,
		"delivery": true, "grocery": true, "laundry": true,
		"queue": true, "anything_else": true, "companionship": true,
	}
	if !validCategories[in.Category] {
		log.Printf("[createTask] invalid category: %q", in.Category)
		c.JSON(400, gin.H{"error": "invalid category"})
		return
	}
	if in.PrepayAmountCents < 0 {
		in.PrepayAmountCents = 0
	}

	// schedule
	var when *time.Time
	if in.IsImmediate {
		now := time.Now()
		when = &now
	} else if s := strings.TrimSpace(in.ScheduledAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			c.JSON(400, gin.H{"error": "scheduled_at must be RFC3339"})
			return
		}
		when = &t
	}

	// auth（用 email 反查 canonical users.id）
	email := strings.TrimSpace(c.GetString("email"))
	if email == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()
	tx, err := sqldb.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		log.Printf("[createTask] BeginTx error: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer func() { _ = tx.Rollback() }()

	// 1) 取得或建立 users（以 email 唯一）
	var uid string
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM public.users WHERE email=$1 LIMIT 1`, email).Scan(&uid)
	if err == sql.ErrNoRows {
		// 沒有就建（讓 DB 產生 id）
		if err := tx.QueryRowContext(ctx, `
      INSERT INTO public.users (email, name)
      VALUES ($1, $2)
      RETURNING id::text
    `, email, deriveName(email)).Scan(&uid); err != nil {
			log.Printf("[users.insert] %v", err)
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
	} else if err != nil {
		log.Printf("[users.lookup] %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}

	// 2) 對齊 profiles：若 profiles(email) 存在但 id != users.id，直接把 profiles.id 改成 users.id
	var profID string
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM public.profiles WHERE email=$1 LIMIT 1`, email).Scan(&profID)
	switch {
	case err == sql.ErrNoRows:
		// 沒有 profile → 建一筆，用 users.id
		if _, err := tx.ExecContext(ctx, `
      INSERT INTO public.profiles (id, email, name, created_at, updated_at)
      VALUES ($1::uuid, $2, $3, now(), now())
    `, uid, email, deriveName(email)); err != nil {
			log.Printf("[createTask] profiles.insert error: %v", err)
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
	case err == nil:
		if profID != uid {
			// 先確定 users 有這個 uid（理論上一定有）
			var exists bool
			_ = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.users WHERE id=$1::uuid)`, uid).Scan(&exists)
			if !exists {
				log.Printf("[createTask] profiles.align users missing uid=%s", uid)
				c.JSON(500, gin.H{"error": "db error"})
				return
			}
			// 直接把 profiles 的主鍵 id 改成 users.id
			if _, err := tx.ExecContext(ctx, `
        UPDATE public.profiles
        SET id = $1::uuid, updated_at = now()
        WHERE id = $2::uuid
      `, uid, profID); err != nil {
				log.Printf("[createTask] profiles.align.update-id error: %v", err)
				c.JSON(500, gin.H{"error": "db error"})
				return
			}
		}
	default:
		log.Printf("[createTask] profiles.lookup error: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}

	// 3) 插入 task（用 users.id / profiles.id 對齊後的 uid）
	var taskID string
	var createdAt time.Time
	transport := in.TransportRequired
	if transport == "" {
		transport = "none"
	}
	if err := tx.QueryRowContext(ctx, `
    INSERT INTO public.tasks
      (title,description,category,location_text,
       estimated_minutes,prepay_amount_cents,is_immediate,scheduled_at,
       requester, requester_id, status, assigned_to, assigned_to_id,
       transport_required)
    VALUES
      ($1,$2,$3,$4,
       $5,$6,$7,$8,
       $9, $10::uuid, 'open', '', NULL,
       $11)
    RETURNING id, created_at
  `, in.Title, in.Description, in.Category, in.LocationText,
		in.EstimatedMinutes, in.PrepayAmountCents, in.IsImmediate, when,
		email, uid, transport,
	).Scan(&taskID, &createdAt); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok {
			log.Printf("[tasks.insert] code=%s tbl=%s col=%s detail=%s where=%s msg=%s",
				pgErr.Code, pgErr.TableName, pgErr.ColumnName, pgErr.Detail, pgErr.Where, pgErr.Message)
		} else {
			log.Printf("[tasks.insert] %v", err)
		}
		c.JSON(500, gin.H{"error": "db error"})
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[createTask] tx.commit error: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}

	c.JSON(201, Task{
		ID: taskID, Title: in.Title, Description: in.Description, Category: in.Category,
		LocationText: in.LocationText, EstimatedMinutes: in.EstimatedMinutes,
		PrepayAmountCents: in.PrepayAmountCents, IsImmediate: in.IsImmediate,
		ScheduledAt: when, Requester: email, RequesterID: uid,
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

// scanTaskFull scans all task columns including travel_time_minutes and total_estimate_minutes.
// Use only with SELECT queries that include those two columns.
func scanTaskFull(rows interface{ Scan(dest ...any) error }) (Task, error) {
	var t Task
	var reqIDNS, assigneeIDNS sql.NullString
	var travelMin, totalEstMin sql.NullInt64

	err := rows.Scan(
		&t.ID, &t.Title, &t.Description, &t.Category, &t.LocationText,
		&t.EstimatedMinutes, &t.PrepayAmountCents, &t.IsImmediate,
		&t.ScheduledAt,
		&t.Requester, &reqIDNS,
		&t.Status, &t.CreatedAt,
		&t.AssignedTo, &assigneeIDNS,
		&travelMin, &totalEstMin,
	)
	if err != nil {
		return t, err
	}

	t.RequesterID = reqIDNS.String
	if assigneeIDNS.Valid {
		id := assigneeIDNS.String
		t.AssignedToID = &id
	}
	if travelMin.Valid {
		v := int(travelMin.Int64)
		t.TravelTimeMinutes = &v
	}
	if totalEstMin.Valid {
		v := int(totalEstMin.Int64)
		t.TotalEstimateMinutes = &v
	}
	return t, nil
}

func listMyTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	limit := parseLimit(c.Query("limit"), 10, 50)
	var beforeCreatedAt *time.Time
	var beforeID *string
	if s := strings.TrimSpace(c.Query("before_created_at")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			beforeCreatedAt = &t
		}
	}
	if s := strings.TrimSpace(c.Query("before_id")); s != "" {
		beforeID = &s
	}

	where := []string{"requester_id = $1::uuid"}
	args := []any{meUID}
	arg := 2
	if beforeCreatedAt != nil && beforeID != nil {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", arg, arg+1))
		args = append(args, *beforeCreatedAt, *beforeID)
		arg += 2
	}

	q := fmt.Sprintf(`
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status, created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where %s
    order by created_at desc, id desc
    limit $%d
  `, strings.Join(where, " and "), arg)
	args = append(args, limit)

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := make([]Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		items = append(items, t)
	}
	var next any = nil
	if len(items) == limit {
		last := items[len(items)-1]
		next = gin.H{
			"before_created_at": last.CreatedAt.Format(time.RFC3339),
			"before_id":         last.ID,
		}
	}
	c.JSON(200, gin.H{"items": items, "next": next})
}

func getTask(c *gin.Context) {
	uid := c.GetString("uid")
	id := c.Param("id")
	ctx := c.Request.Context()

	row := db.QueryRow(ctx, `
    select
      id, title, description, category, location_text,
      estimated_minutes, prepay_amount_cents, is_immediate,
      scheduled_at,
      requester, requester_id,
      status, created_at,
      assigned_to, assigned_to_id,
      travel_time_minutes, total_estimate_minutes
    from public.tasks
    where id = $1::uuid
  `, id)

	t, err := scanTaskFull(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// Fallback: travel columns may not exist in DB yet — retry without them
		log.Printf("[getTask] scanTaskFull failed (id=%s): %v — retrying without travel columns", id, err)
		row2 := db.QueryRow(ctx, `
      select
        id, title, description, category, location_text,
        estimated_minutes, prepay_amount_cents, is_immediate,
        scheduled_at,
        requester, requester_id,
        status, created_at,
        assigned_to, assigned_to_id
      from public.tasks
      where id = $1::uuid
    `, id)
		t, err = scanTask(row2)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			} else {
				log.Printf("[getTask][ERROR] id=%s fallback scan error: %v", id, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			}
			return
		}
	}

	// 檢查是否是指派者
	isAssignee := func() bool {
		if t.AssignedToID == nil {
			return false
		}
		return uid == *t.AssignedToID
	}()

	if t.Status != "open" && uid != t.RequesterID && !isAssignee {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	c.JSON(http.StatusOK, t)
}

func updateTask(c *gin.Context) {
	id := c.Param("id")

	meUID := c.GetString("uid")
	meEmail := c.GetString("email")

	if meUID == "" && meEmail == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()

	// 多抓 requester（email）一起比
	var requesterID, requesterEmail, status string
	if err := db.QueryRow(ctx,
		`select requester_id, requester, status from public.tasks where id=$1`,
		id,
	).Scan(&requesterID, &requesterEmail, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Debug log：之後看 log 就知道為什麼被擋
	log.Printf(
		"[updateTask] id=%s meUID=%s meEmail=%s db.requester_id=%s db.requester=%s status=%s",
		id, meUID, meEmail, requesterID, requesterEmail, status,
	)

	// ✅ 只要「其中一種身分」對得上，就視為 owner
	isOwnerByID := (meUID != "" && requesterID != "" && requesterID == meUID)
	isOwnerByEmail := (meEmail != "" && requesterEmail != "" && strings.EqualFold(requesterEmail, meEmail))

	if !isOwnerByID && !isOwnerByEmail {
		c.JSON(http.StatusForbidden, gin.H{
			"error":        "not your task",
			"me_uid":       meUID,
			"me_email":     meEmail,
			"requester_id": requesterID,
			"requester":    requesterEmail,
		})
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
		in.Category = "quick_errand"
	}
	validCats := map[string]bool{
		"task": true, "companion": true,
		"quick_errand": true, "standard": true, "half_day": true, "full_day": true,
		"delivery": true, "grocery": true, "laundry": true,
		"queue": true, "anything_else": true, "companionship": true,
	}
	if !validCats[in.Category] {
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
        set title=$1,
            description=$2,
            category=$3,
            location_text=$4,
            estimated_minutes=$5,
            prepay_amount_cents=$6,
            is_immediate=$7,
            scheduled_at=$8
        where id=$9
    `,
		in.Title,
		in.Description,
		in.Category,
		in.LocationText,
		in.EstimatedMinutes,
		in.PrepayAmountCents,
		in.IsImmediate,
		when,
		id,
	)
	if err != nil {
		log.Printf("[updateTask] db error: %v", err)
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

	meEmail := c.GetString("email")
	var isVerified bool
	if meEmail != "" {
		_ = db.QueryRow(c.Request.Context(),
			`select coalesce(is_verified_supporter, false) from public.profiles where email = $1`,
			meEmail,
		).Scan(&isVerified)
	} else {
		// fallback: email not in token — try by internal UUID
		_ = db.QueryRow(c.Request.Context(),
			`select coalesce(is_verified_supporter, false) from public.profiles where id = $1::uuid`,
			meUID,
		).Scan(&isVerified)
	}
	log.Printf("[listAvailableTasks] uid=%s email=%s is_verified_supporter=%v", meUID, meEmail, isVerified)
	if !isVerified {
		c.JSON(403, gin.H{"error": "verification_required"})
		return
	}

	limit := parseLimit(c.Query("limit"), 10, 50)
	var beforeCreatedAt *time.Time
	var beforeID *string
	if s := strings.TrimSpace(c.Query("before_created_at")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			beforeCreatedAt = &t
		}
	}
	if s := strings.TrimSpace(c.Query("before_id")); s != "" {
		beforeID = &s
	}

	where := []string{
		"status='open'",
		"assigned_to_id is null",
		"requester_id <> $1::uuid",
	}
	args := []any{meUID}
	arg := 2
	if beforeCreatedAt != nil && beforeID != nil {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", arg, arg+1))
		args = append(args, *beforeCreatedAt, *beforeID)
		arg += 2
	}

	q := fmt.Sprintf(`
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status, created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where %s
    order by created_at desc, id desc
    limit $%d
  `, strings.Join(where, " and "), arg)
	args = append(args, limit)

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := make([]Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		items = append(items, t)
	}

	var next any = nil
	if len(items) == limit {
		last := items[len(items)-1]
		next = gin.H{
			"before_created_at": last.CreatedAt.Format(time.RFC3339),
			"before_id":         last.ID,
		}
	}
	c.JSON(200, gin.H{"items": items, "next": next})
}

func listAssignedTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	limit := parseLimit(c.Query("limit"), 10, 50)
	var beforeCreatedAt *time.Time
	var beforeID *string
	if s := strings.TrimSpace(c.Query("before_created_at")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			beforeCreatedAt = &t
		}
	}
	if s := strings.TrimSpace(c.Query("before_id")); s != "" {
		beforeID = &s
	}

	where := []string{
		"assigned_to_id = $1::uuid",
		"status='open'",
	}
	args := []any{meUID}
	arg := 2
	if beforeCreatedAt != nil && beforeID != nil {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", arg, arg+1))
		args = append(args, *beforeCreatedAt, *beforeID)
		arg += 2
	}

	q := fmt.Sprintf(`
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status, created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where %s
    order by created_at desc, id desc
    limit $%d
  `, strings.Join(where, " and "), arg)
	args = append(args, limit)

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := make([]Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		items = append(items, t)
	}
	var next any = nil
	if len(items) == limit {
		last := items[len(items)-1]
		next = gin.H{
			"before_created_at": last.CreatedAt.Format(time.RFC3339),
			"before_id":         last.ID,
		}
	}
	c.JSON(200, gin.H{"items": items, "next": next})
}

func listDoneTasks(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	limit := parseLimit(c.Query("limit"), 10, 50)
	var beforeCreatedAt *time.Time
	var beforeID *string
	if s := strings.TrimSpace(c.Query("before_created_at")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			beforeCreatedAt = &t
		}
	}
	if s := strings.TrimSpace(c.Query("before_id")); s != "" {
		beforeID = &s
	}

	where := []string{"assigned_to_id = $1::uuid", "status='completed'"}
	args := []any{meUID}
	arg := 2
	if beforeCreatedAt != nil && beforeID != nil {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", arg, arg+1))
		args = append(args, *beforeCreatedAt, *beforeID)
		arg += 2
	}

	q := fmt.Sprintf(`
    select id,title,description,category,location_text,
           estimated_minutes,prepay_amount_cents,is_immediate,
           scheduled_at,
           requester, requester_id,
           status, created_at,
           assigned_to, assigned_to_id
    from public.tasks
    where %s
    order by created_at desc, id desc
    limit $%d
  `, strings.Join(where, " and "), arg)
	args = append(args, limit)

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := make([]Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		items = append(items, t)
	}
	var next any = nil
	if len(items) == limit {
		last := items[len(items)-1]
		next = gin.H{
			"before_created_at": last.CreatedAt.Format(time.RFC3339),
			"before_id":         last.ID,
		}
	}
	c.JSON(200, gin.H{"items": items, "next": next})
}

func listMyPostedClosed(c *gin.Context) {
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}

	limit := parseLimit(c.Query("limit"), 10, 50)
	var beforeCreatedAt *time.Time
	var beforeID *string
	if s := strings.TrimSpace(c.Query("before_created_at")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			beforeCreatedAt = &t
		}
	}
	if s := strings.TrimSpace(c.Query("before_id")); s != "" {
		beforeID = &s
	}

	// 我發的 + 已關閉（完成或取消）
	where := []string{
		"requester_id = $1::uuid",
		"status in ('completed','cancelled')",
	}
	args := []any{meUID}
	arg := 2

	// keyset： (created_at, id) < (cursor)
	if beforeCreatedAt != nil && beforeID != nil {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", arg, arg+1))
		args = append(args, *beforeCreatedAt, *beforeID)
		arg += 2
	}

	q := fmt.Sprintf(`
		select id,title,description,category,location_text,
		       estimated_minutes,prepay_amount_cents,is_immediate,
		       scheduled_at,
		       requester, requester_id,
		       status, created_at,
		       assigned_to, assigned_to_id
		from public.tasks
		where %s
		order by created_at desc, id desc
		limit $%d
	`, strings.Join(where, " and "), arg)
	args = append(args, limit)

	ctx := c.Request.Context()
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := make([]Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			c.JSON(500, gin.H{"error": "scan error"})
			return
		}
		items = append(items, t)
	}
	var next any = nil
	if len(items) == limit {
		last := items[len(items)-1]
		next = gin.H{
			"before_created_at": last.CreatedAt.Format(time.RFC3339),
			"before_id":         last.ID,
		}
	}
	c.JSON(200, gin.H{"items": items, "next": next})
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
	var requesterID, status, acceptTaskTitle string
	var assignedToID *string
	err := db.QueryRow(ctx, `
    select requester_id, status, assigned_to_id, COALESCE(title,'')
    from public.tasks where id=$1
  `, id).Scan(&requesterID, &status, &assignedToID, &acceptTaskTitle)
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

	log.Printf("[debug] about to notify requester for task %s", id)
	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:        id,
		Type:          "ORDER_ACCEPTED",
		Title:         "Your task has been accepted",
		Body:          fmt.Sprintf("%s has accepted your task.", displayName(meEmail)),
		SupporterName: displayName(meEmail),
		TaskTitle:     acceptTaskTitle,
	})
	// TODO: WhatsApp notification here
	getTask(c)
}

// -------- WorkLog handlers --------
const overtimeRateCents = 50 // $0.50/min for every minute over the 15-min included block

func clockIn(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	meEmail := c.GetString("email")
	log.Printf("[debug] clockIn called for task %s uid %s", taskID, meUID)
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// 用 uuid 判斷是否為受派者
	var status, clockInTaskTitle string
	var assignedToID sql.NullString
	if err := db.QueryRow(ctx, `
      select assigned_to_id, status, COALESCE(title,'')
      from public.tasks
      WHERE id = $1::uuid
    `, taskID).Scan(&assignedToID, &status, &clockInTaskTitle); err != nil {
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

	log.Printf("[debug] clockIn about to notify for task %s", taskID)
	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "CLOCK_IN",
		Title:         "Your supporter has clocked in",
		Body:          "The timer is now running. You can track progress in your dashboard.",
		SupporterName: displayName(meEmail),
		TaskTitle:     clockInTaskTitle,
		ClockInTime:   startAt.Format("15:04"),
	})
	// TODO: WhatsApp notification here

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
      WHERE id = $1::uuid
      returning start_at, end_at, created_at, updated_at
    `, wlID).Scan(&startAt, &endAt, &createdAt, &updatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// Compute session duration and running total for notification email
	sessionDur := endAt.Sub(startAt).Round(time.Minute)
	sessionHrs := int(sessionDur.Hours())
	sessionMins := int(sessionDur.Minutes()) % 60
	sessionStr := fmt.Sprintf("%d min", int(sessionDur.Minutes()))
	if sessionHrs > 0 {
		sessionStr = fmt.Sprintf("%dh %dmin", sessionHrs, sessionMins)
	}
	totalMin, _ := totalClosedMinutes(ctx, taskID)
	totalStr := fmt.Sprintf("%d min", totalMin)
	if h := totalMin / 60; h > 0 {
		totalStr = fmt.Sprintf("%dh %dmin", h, totalMin%60)
	}
	var clockOutTaskTitle string
	_ = db.QueryRow(ctx, `SELECT COALESCE(title,'') FROM public.tasks WHERE id=$1::uuid`, taskID).Scan(&clockOutTaskTitle)

	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "CLOCK_OUT",
		Title:         "Your supporter has clocked out",
		Body:          fmt.Sprintf("Session ended. Total time logged so far: %s", totalStr),
		SupporterName: displayName(meEmail),
		TaskTitle:     clockOutTaskTitle,
		SessionTime:   sessionStr,
		TotalLogged:   totalStr,
	})
	// TODO: WhatsApp notification here

	c.JSON(http.StatusOK, WorkLog{
		ID: wlID, TaskID: taskID, User: meEmail,
		Start: startAt, End: &endAt, CreatedAt: createdAt, UpdatedAt: updatedAt,
	})
}

// POST /tasks/:id/gps-ping — assignee saves current location while clocked in
func saveGpsPing(c *gin.Context) {
	taskID := c.Param("id")
	meEmail := c.GetString("email")
	uid := c.GetString("uid")
	if meEmail == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// must have an active clock-in session
	var exists bool
	_ = db.QueryRow(ctx, `
		select exists(
			select 1 from public.worklogs
			where task_id=$1 and "user"=$2 and end_at is null
		)
	`, taskID, meEmail).Scan(&exists)
	if !exists {
		c.JSON(http.StatusForbidden, gin.H{"error": "not clocked in"})
		return
	}

	var in struct {
		Lat      float64 `json:"lat" binding:"required"`
		Lng      float64 `json:"lng" binding:"required"`
		Accuracy *int    `json:"accuracy"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, err := db.Exec(ctx, `
		insert into public.task_gps_pings(task_id, user_id, lat, lng, accuracy)
		values ($1::uuid, $2::uuid, $3, $4, $5)
	`, taskID, uid, in.Lat, in.Lng, in.Accuracy)
	if err != nil {
		log.Printf("[gps-ping] err=%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /tasks/:id/gps-latest — requester or assignee fetches last known location
func getLatestGps(c *gin.Context) {
	taskID := c.Param("id")
	meEmail := c.GetString("email")
	if meEmail == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// only requester or assignee can view
	var requesterEmail, assignedEmail *string
	err := db.QueryRow(ctx, `
		select requester, assigned_to from public.tasks where id=$1::uuid
	`, taskID).Scan(&requesterEmail, &assignedEmail)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	allowed := (requesterEmail != nil && *requesterEmail == meEmail) ||
		(assignedEmail != nil && *assignedEmail == meEmail)
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var lat, lng float64
	var accuracy *int
	var createdAt time.Time
	err = db.QueryRow(ctx, `
		select lat, lng, accuracy, created_at
		from public.task_gps_pings
		where task_id=$1::uuid
		order by created_at desc limit 1
	`, taskID).Scan(&lat, &lng, &accuracy, &createdAt)
	if err != nil {
		// no ping yet
		c.JSON(http.StatusOK, nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"lat":        lat,
		"lng":        lng,
		"accuracy":   accuracy,
		"created_at": createdAt,
	})
}

func getWorklogs(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid") // ✅ 用 uid
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	// 用 uuid 欄位檢查權限
	var requesterID string
	var assignedToID *string
	if err := db.QueryRow(ctx, `
        select requester_id, assigned_to_id
        from public.tasks
        WHERE id = $1::uuid
    `, taskID).Scan(&requesterID, &assignedToID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if meUID != requesterID && (assignedToID == nil || meUID != *assignedToID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	// 下面原本的查詢不需改
	rows, err := db.Query(ctx, `
        select id,task_id,"user",start_at,end_at,created_at,updated_at
        from public.worklogs
        where task_id=$1
        order by start_at asc
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
          select 1 from public.worklogs
          where task_id=$1 and end_at is null
        )
    `, taskID).Scan(&hasOpen)

	c.JSON(http.StatusOK, gin.H{
		"items":            items,
		"total_minutes":    totalMin,
		"total_cost_cents": calcTaskCostCents(ctx, taskID, totalMin),
		"has_open":         hasOpen,
	})
}

// POST /tasks/:id/completion-photo — upload a completion photo to Supabase Storage.
// Returns { url: "https://..." } for use in the complete request.
func uploadTaskCompletionPhoto(c *gin.Context) {
	taskID := c.Param("id")
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	// Verify caller is requester or assignee
	var requesterID, assignedToID sql.NullString
	if err := db.QueryRow(c.Request.Context(), `
		SELECT requester_id, assigned_to_id FROM public.tasks WHERE id=$1
	`, taskID).Scan(&requesterID, &assignedToID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if (!requesterID.Valid || requesterID.String != uid) &&
		(!assignedToID.Valid || assignedToID.String != uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 5<<20)

	f, hdr, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required (multipart/form-data; field name 'file')"})
		return
	}
	defer f.Close()

	contentType := hdr.Header.Get("Content-Type")
	// Normalise browser quirks (image/jpg is invalid; some browsers omit Content-Type entirely).
	if contentType == "application/octet-stream" || contentType == "" || contentType == "image/jpg" {
		switch strings.ToLower(filepath.Ext(hdr.Filename)) {
		case ".jpg", ".jpeg":
			contentType = "image/jpeg"
		case ".png":
			contentType = "image/png"
		case ".webp":
			contentType = "image/webp"
		case ".gif":
			contentType = "image/gif"
		case ".heic":
			contentType = "image/heic"
		case ".heif":
			contentType = "image/heif"
		default:
			contentType = "image/jpeg" // safe fallback for camera photos with no extension
		}
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, f); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read file failed"})
		return
	}

	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	key := fmt.Sprintf("completions/%s/%d%s", taskID, time.Now().Unix(), ext)
	const bucket = "task-completions"

	base := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
	serviceKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")

	// Sanity-check env vars are present
	if base == "" || serviceKey == "" {
		log.Printf("[completion-photo][ERR] missing env: SUPABASE_PROJECT_URL=%q keyLen=%d", base, len(serviceKey))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage not configured"})
		return
	}
	log.Printf("[completion-photo] projectURL=%s keyPrefix=%.8s keyLen=%d", base, serviceKey, len(serviceKey))

	// Direct upload using service role key — bypasses RLS, no signed-URL round-trip.
	// Supabase Storage sits behind Kong which requires BOTH Authorization and apikey headers.
	uploadURL := fmt.Sprintf("%s/storage/v1/object/%s/%s", base, bucket, key)
	log.Printf("[completion-photo] uploading key=%s ct=%s size=%d", key, contentType, buf.Len())

	req, err := http.NewRequest("POST", uploadURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "build request failed"})
		return
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+serviceKey)
	req.Header.Set("apikey", serviceKey) // Kong gateway requires this
	req.Header.Set("x-upsert", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[completion-photo][upload][ERR] %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "upload failed"})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	log.Printf("[completion-photo] supabase response status: %d", resp.StatusCode)
	log.Printf("[completion-photo] supabase response body: %s", string(b))
	if resp.StatusCode >= 300 {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  fmt.Sprintf("upload failed (%d)", resp.StatusCode),
			"detail": string(b), // visible in browser devtools for debugging
		})
		return
	}
	log.Printf("[completion-photo][upload][OK] key=%s status=%d", key, resp.StatusCode)

	publicURL := fmt.Sprintf("%s/storage/v1/object/public/%s/%s", base, bucket, key)
	log.Printf("[completion-photo] task=%s url=%s", taskID, publicURL)

	c.JSON(http.StatusOK, gin.H{"url": publicURL})
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

	var body struct {
		CompletionPhotoURL string `json:"completion_photo_url"`
		CompletionNote     string `json:"completion_note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(body.CompletionPhotoURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Completion photo is required"})
		return
	}

	var status, assigneeEmail, completeTaskTitle string
	var requesterID, assignedToID sql.NullString
	if err := db.QueryRow(ctx, `
      select requester_id, assigned_to_id, assigned_to, status, COALESCE(title,'')
      from public.tasks where id=$1
    `, taskID).Scan(&requesterID, &assignedToID, &assigneeEmail, &status, &completeTaskTitle); err != nil {
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

	// calculate final total across all sessions
	totalMin, _ := totalClosedMinutes(ctx, taskID)
	totalCents := calcTaskCostCents(ctx, taskID, totalMin)

	completeHrs := totalMin / 60
	completeMins := totalMin % 60
	completeTotalStr := fmt.Sprintf("%d min", totalMin)
	if completeHrs > 0 {
		completeTotalStr = fmt.Sprintf("%dh %dmin", completeHrs, completeMins)
	}
	completeCostStr := fmt.Sprintf("$%.2f", float64(totalCents)/100.0)

	// TODO: trigger Stripe capture here
	// e.g. stripe.CapturePaymentIntent(task.PaymentIntentID, totalCents)

	if _, err := db.Exec(ctx, `
		UPDATE public.tasks
		SET status='completed', completion_photo_url=$2, completion_note=$3, completed_at=now()
		WHERE id = $1::uuid`,
		taskID, body.CompletionPhotoURL, body.CompletionNote); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	log.Printf("[completeTask] sending completion email for task %s", taskID)

	// Email requester: summary with time logged + final cost
	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:             taskID,
		Type:               "COMPLETED",
		Title:              "Your task has been completed",
		Body:               "Everything is done. Please leave a rating when you have a moment.",
		SupporterName:      displayName(assigneeEmail),
		TaskTitle:          completeTaskTitle,
		TotalLogged:        completeTotalStr,
		FinalCost:          completeCostStr,
		CompletionPhotoURL: body.CompletionPhotoURL,
		CompletionNote:     body.CompletionNote,
	})
	// TODO: WhatsApp notification here

	// Email supporter: confirmation with time logged + earnings
	notifyAssignee(c, notify.CreateNotificationInput{
		TaskID:      taskID,
		Type:        "COMPLETED_SUPPORTER",
		Title:       "Task marked complete — great work!",
		Body:        fmt.Sprintf("The task has been marked complete. Time logged: %s", completeTotalStr),
		TaskTitle:   completeTaskTitle,
		TotalLogged: completeTotalStr,
		FinalCost:   completeCostStr,
	})
	// TODO: WhatsApp notification here

	getTask(c)
}

// POST /tasks/:id/cancel
// 只有作者(requester)可取消；任務須為 open；若有未結束工時(end_at is null)不可取消。
// 已有結束工時則：bill = total_minutes * rate；refund = max(prepay - bill, 0)。
// 取已結束工時總分鐘（向上取整，每段至少 1 分鐘），只算 end_at 有值的
func totalClosedMinutes(ctx context.Context, taskID string) (int, error) {
	var total int
	err := db.QueryRow(ctx, `
		with x as (
			select ceil(extract(epoch from (end_at - start_at))/60.0)::int as m
			from public.worklogs
			where task_id=$1 and end_at is not null and end_at > start_at
		)
		select coalesce(sum(greatest(m,1)),0) from x
	`, taskID).Scan(&total)
	return total, err
}

// calcTaskCostCents returns the service charge in cents for totalMinutes of work on taskID.
// Base fee: $25 (companionship/companion), $18 (estimated_minutes > 90), $12 (everything else).
// Overtime: $0.50/min for every minute worked beyond the first 15.
func calcTaskCostCents(ctx context.Context, taskID string, totalMinutes int) int {
	var category string
	var estimatedMinutes int
	_ = db.QueryRow(ctx,
		`SELECT COALESCE(category,''), COALESCE(estimated_minutes,0) FROM public.tasks WHERE id=$1::uuid`,
		taskID,
	).Scan(&category, &estimatedMinutes)

	baseCents := 1200
	switch {
	case category == "companionship" || category == "companion":
		baseCents = 2500
	case estimatedMinutes > 90:
		baseCents = 1800
	}

	overtimeCents := 0
	if totalMinutes > 15 {
		overtimeCents = (totalMinutes - 15) * overtimeRateCents
	}
	return baseCents + overtimeCents
}

func cancelTask(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	// 讀取 reason
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason required"})
		return
	}

	ctx := c.Request.Context()

	// 讀任務狀態與擁有者
	var requesterID, status, cancelTaskTitle string
	var assignedToID *string
	err := db.QueryRow(ctx, `
		select requester_id, status, assigned_to_id, COALESCE(title,'')
		from public.tasks
		WHERE id = $1::uuid
	`, taskID).Scan(&requesterID, &status, &assignedToID, &cancelTaskTitle)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// 權限：只有作者能取消
	if requesterID != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "only requester can cancel"})
		return
	}

	// 狀態檢查
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task already closed"})
		return
	}

	// 如果你規則是「未接單前才可取消」就擋這裡（也可改成允許已接單但未開工）：
	if assignedToID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot cancel after it has been accepted"})
		return
	}

	// 是否有未結束工時
	var hasOpen bool
	_ = db.QueryRow(ctx, `
		select exists (
			select 1 from public.worklogs
			where task_id=$1 and end_at is null
		)
	`, taskID).Scan(&hasOpen)
	if hasOpen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot cancel with an active work session (clock out first)"})
		return
	}

	// 計算費用（已結束的總分鐘 * 單價）
	totalMin, err := totalClosedMinutes(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "calc error"})
		return
	}
	var prepayCents int
	if err := db.QueryRow(ctx, `
		select coalesce(prepay_amount_cents,0) from public.tasks where id=$1
	`, taskID).Scan(&prepayCents); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	billCents := calcTaskCostCents(ctx, taskID, totalMin)
	refundCents := prepayCents - billCents
	if refundCents < 0 {
		refundCents = 0
	}

	// 寫入取消狀態 + 理由（建議你在 tasks 加欄位：cancel_reason text, cancelled_at timestamptz）
	_, err = db.Exec(ctx, `
		update public.tasks
		set status='cancelled',
		    cancel_reason = $2,
		    cancelled_at = now()
		where id=$1
	`, taskID, in.Reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// （可選）寫入 audit_logs（若你有這張表）
	// _, _ = db.Exec(ctx, `
	// 	insert into public.audit_logs(task_id, actor_id, action, reason, meta)
	// 	values ($1,$2,'TASK_CANCELLED',$3,jsonb_build_object(
	// 		'total_minutes', $4, 'bill_cents', $5, 'refund_cents', $6
	// 	))
	// `, taskID, meUID, in.Reason, totalMin, billCents, refundCents)

	// Email requester: cancellation confirmation with reason
	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "CANCELLED",
		Title:     "Task cancelled",
		Body:      fmt.Sprintf("Your task has been cancelled. Reason: %s", in.Reason),
		TaskTitle: cancelTaskTitle,
	})
	// TODO: WhatsApp notification here

	// Email supporter (if task was already accepted before cancellation policy changes)
	notifyAssignee(c, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "CANCELLED",
		Title:     "A task you accepted has been cancelled",
		Body:      fmt.Sprintf("The requester cancelled the task. Reason: %s", in.Reason),
		TaskTitle: cancelTaskTitle,
	})
	// TODO: WhatsApp notification here

	// 前端期望的回傳格式
	c.JSON(http.StatusOK, gin.H{
		"total_minutes": totalMin,
		"bill_cents":    billCents,
		"refund_cents":  refundCents,
	})
}

// -------- Travel Estimate --------

func estimateTravel(c *gin.Context) {
	var body struct {
		SupporterLat float64 `json:"supporter_lat"`
		SupporterLng float64 `json:"supporter_lng"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.SupporterLat == 0 || body.SupporterLng == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "supporter_lat and supporter_lng required"})
		return
	}

	taskID := c.Param("id")
	ctx := c.Request.Context()

	var locationText string
	var estimatedMinutes int
	if err := db.QueryRow(ctx,
		"SELECT COALESCE(location_text,''), estimated_minutes FROM public.tasks WHERE id = $1::uuid",
		taskID,
	).Scan(&locationText, &estimatedMinutes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		}
		return
	}

	if locationText == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "task has no location"})
		return
	}

	travelMinutes, err := helpers.GetTravelTime(body.SupporterLat, body.SupporterLng, locationText)
	if err != nil {
		log.Printf("[estimate-travel] GetTravelTime error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not calculate travel time"})
		return
	}

	total := estimatedMinutes + travelMinutes

	_, _ = db.Exec(ctx, `
		UPDATE public.tasks
		SET travel_time_minutes = $1, total_estimate_minutes = $2
		WHERE id = $3::uuid
	`, travelMinutes, total, taskID)

	c.JSON(http.StatusOK, gin.H{
		"travel_minutes": travelMinutes,
		"task_minutes":   estimatedMinutes,
		"total_minutes":  total,
		"display":        fmt.Sprintf("~ %d min (%d min task + %d min travel)", total, estimatedMinutes, travelMinutes),
	})
}

// -------- Reviews --------

type Review struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	ReviewerID  string     `json:"reviewer_id"`
	SupporterID string     `json:"supporter_id"`
	Stars       int        `json:"stars"`
	ValueRating string     `json:"value_rating"`
	WouldRehire *bool      `json:"would_rehire"`
	Comment     string     `json:"comment"`
	CreatedAt   time.Time  `json:"created_at"`
}

func createReview(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var in struct {
		Stars       int     `json:"stars"`
		ValueRating string  `json:"value_rating"`
		WouldRehire *bool   `json:"would_rehire"`
		Comment     string  `json:"comment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if in.Stars < 1 || in.Stars > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stars must be between 1 and 5"})
		return
	}
	validValueRatings := map[string]bool{"not_worth": true, "fair": true, "great": true}
	if in.ValueRating != "" && !validValueRatings[in.ValueRating] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid value_rating"})
		return
	}

	ctx := c.Request.Context()

	// Load task — verify requester and status
	var requesterID, supporterID, status string
	if err := db.QueryRow(ctx, `
		SELECT COALESCE(requester_id::text,''), COALESCE(assigned_to_id::text,''), status
		FROM public.tasks WHERE id = $1::uuid
	`, taskID).Scan(&requesterID, &supporterID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		}
		return
	}
	if requesterID != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the requester can review"})
		return
	}
	if status != "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task must be completed before reviewing"})
		return
	}
	if supporterID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no supporter to review"})
		return
	}

	// One review per task
	var existing bool
	_ = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.reviews WHERE task_id=$1::uuid)`, taskID).Scan(&existing)
	if existing {
		c.JSON(http.StatusConflict, gin.H{"error": "already reviewed"})
		return
	}

	var r Review
	if err := db.QueryRow(ctx, `
		INSERT INTO public.reviews (task_id, reviewer_id, supporter_id, stars, value_rating, would_rehire, comment)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
		RETURNING id, task_id, reviewer_id::text, supporter_id::text, stars, value_rating, would_rehire, comment, created_at
	`, taskID, meUID, supporterID, in.Stars, in.ValueRating, in.WouldRehire, strings.TrimSpace(in.Comment),
	).Scan(&r.ID, &r.TaskID, &r.ReviewerID, &r.SupporterID, &r.Stars, &r.ValueRating, &r.WouldRehire, &r.Comment, &r.CreatedAt); err != nil {
		log.Printf("[createReview] db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusCreated, r)
}

func listProfileReviews(c *gin.Context) {
	supporterID := c.Param("id")
	ctx := c.Request.Context()

	type ReviewItem struct {
		ID          string    `json:"id"`
		TaskID      string    `json:"task_id"`
		TaskTitle   string    `json:"task_title"`
		Stars       int       `json:"stars"`
		ValueRating string    `json:"value_rating"`
		WouldRehire *bool     `json:"would_rehire"`
		Comment     string    `json:"comment"`
		CreatedAt   time.Time `json:"created_at"`
	}

	rows, err := db.Query(ctx, `
		SELECT r.id, r.task_id, COALESCE(t.title,''), r.stars,
		       COALESCE(r.value_rating,''), r.would_rehire,
		       COALESCE(r.comment,''), r.created_at
		FROM public.reviews r
		LEFT JOIN public.tasks t ON t.id = r.task_id
		WHERE r.supporter_id = $1::uuid
		ORDER BY r.created_at DESC
	`, supporterID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := []ReviewItem{}
	var totalStars int
	for rows.Next() {
		var item ReviewItem
		if err := rows.Scan(&item.ID, &item.TaskID, &item.TaskTitle, &item.Stars,
			&item.ValueRating, &item.WouldRehire, &item.Comment, &item.CreatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		totalStars += item.Stars
		items = append(items, item)
	}

	var avgStars *float64
	if len(items) > 0 {
		v := float64(totalStars) / float64(len(items))
		avgStars = &v
	}

	c.JSON(http.StatusOK, gin.H{
		"reviews":    items,
		"count":      len(items),
		"avg_stars":  avgStars,
	})
}

// -------- Notifications --------
func OnOrderAccepted(ctx context.Context, db *sql.DB, taskID, requesterID, supporterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, TaskID: taskID, Type: "ORDER_ACCEPTED",
		Title:     "Your request was accepted",
		Body:      "Your supporter has accepted the job. You'll be notified when they clock in.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

func OnClockIn(ctx context.Context, db *sql.DB, taskID, requesterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, TaskID: taskID, Type: "CLOCK_IN",
		Title:     "Supporter clocked in",
		Body:      "The job has started. You can track progress in your dashboard.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

func OnClockOut(ctx context.Context, db *sql.DB, taskID, requesterID, requesterEmail string) {
	if err := notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, TaskID: taskID, Type: "CLOCK_OUT",
		Title:     "Supporter clocked out",
		Body:      "Work session ended. We'll compute the bill and show the breakdown.",
		SendEmail: true, EmailTo: requesterEmail,
	}); err != nil {
		log.Printf("[notify] create failed: %v", err)
	}
}

func OnCompleted(ctx context.Context, db *sql.DB, taskID, requesterID, requesterEmail string) {
	notify.Create(ctx, notify.CreateNotificationInput{
		DB: db, UserID: requesterID, TaskID: taskID, Type: "COMPLETED",
		Title:     "Job completed",
		Body:      "Everything is done. Please leave a rating when you have a moment.",
		SendEmail: true, EmailTo: requesterEmail,
	})
}

func RegisterNotificationRoutes(g *gin.Engine, db *sql.DB) {
	grp := g.Group("/")
	// 以前是 authMiddleware()/dualAuth()，請改成 tryAuth()：不通過也不 401
	grp.Use(tryAuth(sqldb))

	// GET /notifications
	grp.GET("/notifications", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.Status(http.StatusNoContent) // 或 c.JSON(http.StatusOK, []NotificationDTO{})
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

		log.Printf("[notifications][GET] uid=%s sql=%s args=%v", me, q, args)

		rows, err := db.QueryContext(c.Request.Context(), q, args...)
		if err != nil {
			log.Printf("[notifications][GET][query] err=%v", err)
			c.JSON(http.StatusOK, []NotificationDTO{})
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
				log.Printf("[notifications][GET][scan] err=%v", err)
				c.JSON(http.StatusOK, []NotificationDTO{})
				return
			}
			out = append(out, n)
		}
		c.JSON(http.StatusOK, out)
	})

	grp.PATCH("/notifications/:id/read", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		id := c.Param("id")
		_, err := db.ExecContext(c.Request.Context(),
			`UPDATE public.notifications SET unread = false WHERE id = $1::uuid AND user_id = $2::uuid`,
			id, me,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	grp.POST("/notifications/mark-read-all", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		_, err := db.ExecContext(c.Request.Context(),
			`UPDATE public.notifications SET unread = false WHERE user_id = $1::uuid AND unread = true`,
			me,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	grp.DELETE("/notifications/:id", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		id := c.Param("id")
		_, err := db.ExecContext(c.Request.Context(),
			`DELETE FROM public.notifications WHERE id = $1::uuid AND user_id = $2::uuid`,
			id, me,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	grp.DELETE("/notifications", func(c *gin.Context) {
		me := c.GetString("uid")
		if me == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		if c.Query("read") != "true" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing read=true"})
			return
		}
		_, err := db.ExecContext(c.Request.Context(),
			`DELETE FROM public.notifications WHERE user_id = $1::uuid AND unread = false`,
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
		SELECT COALESCE(requester_id::text,''), COALESCE(requester,'')
		FROM public.tasks
		WHERE id = $1
	`, taskID).Scan(&uid, &email)
	return
}

// displayName derives a human-readable name from an email address.
// "john.doe@example.com" → "John Doe"
func displayName(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return cases.Title(language.English).String(
			strings.ReplaceAll(email[:i], ".", " "),
		)
	}
	return email
}

// notifyRequesterRich looks up the requester uid+email for the given task and
// calls notify.Create with all extra template fields pre-filled in `in`.
// Callers must set: TaskID, Type, Title, Body, and any SupporterName/TaskTitle/etc.
func notifyRequesterRich(c *gin.Context, in notify.CreateNotificationInput) {
	ctx := c.Request.Context()
	uid, email, err := requesterUIDAndEmail(ctx, in.TaskID)
	if err != nil {
		log.Printf("[notify][skip] lookup requester uid/email task=%s: %v", in.TaskID, err)
		return
	}
	log.Printf("[notify][rich] task=%s type=%s uid=%q email=%q sendEmail=%v", in.TaskID, in.Type, uid, email, email != "")
	in.DB = sqldb
	in.UserID = uid
	in.SendEmail = email != ""
	in.EmailTo = email
	if err := notify.Create(ctx, in); err != nil {
		log.Printf("[notify][ERROR] %s: %v", in.Type, err)
	}
	// TODO: WhatsApp notification here
}

// notifyAssignee looks up the assignee uid+email for the given task and calls
// notify.Create. No-op if the task has no assignee.
func notifyAssignee(c *gin.Context, in notify.CreateNotificationInput) {
	ctx := c.Request.Context()
	var uid, email string
	if err := sqldb.QueryRowContext(ctx, `
		SELECT COALESCE(assigned_to_id::text,''), COALESCE(assigned_to,'')
		FROM public.tasks WHERE id = $1
	`, in.TaskID).Scan(&uid, &email); err != nil || uid == "" || email == "" {
		return
	}
	in.DB = sqldb
	in.UserID = uid
	in.SendEmail = email != ""
	in.EmailTo = email
	if err := notify.Create(ctx, in); err != nil {
		log.Printf("[notify][ERROR] %s assignee: %v", in.Type, err)
	}
	// TODO: WhatsApp notification here
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
		TaskID:    taskID,
		Type:      ntype,
		Title:     title,
		Body:      body,
		SendEmail: email != "",
		EmailTo:   email,
	}); err != nil {
		log.Printf("[notify][ERROR] %s: %v", ntype, err)
	}
}
func dualAuth(db *sql.DB) gin.HandlerFunc {
	hmacSecret := []byte(os.Getenv("SESSION_JWT_SECRET"))

	return func(c *gin.Context) {
		// 1) 先試 Cookie（Google OAuth → sub 已是 internal UUID）
		if cookie, err := c.Cookie("hora_session"); err == nil && cookie != "" {
			tok, err := jwt.Parse(cookie, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return hmacSecret, nil
			})
			if err == nil && tok.Valid {
				if claims, ok := tok.Claims.(jwt.MapClaims); ok {
					if sub, _ := claims["sub"].(string); sub != "" {
						c.Set("uid", sub) // internal UUID
						if email, _ := claims["email"].(string); email != "" {
							c.Set("email", email)
						}
						c.Next()
						return
					}
				}
			} else {
				// 清壞 cookie（避免卡住）
				http.SetCookie(c.Writer, &http.Cookie{
					Name: "hora_session", Value: "", Path: "/", MaxAge: -1,
					HttpOnly: true, Secure: false,
				})
			}
		}

		// 2) 再試 Bearer（Supabase → sub 是 Supabase UUID；需翻譯成 internal UUID）
		authz := c.GetHeader("Authorization")
		if strings.HasPrefix(authz, "Bearer ") {
			raw := strings.TrimSpace(authz[7:])
			token, err := jwt.Parse(raw, jwks.Keyfunc)
			if err == nil && token != nil && token.Valid {
				if claims, ok := token.Claims.(jwt.MapClaims); ok {
					// Supabase sub（uuid）
					if extSub, _ := claims["sub"].(string); extSub != "" {
						email, _ := claims["email"].(string)

						// Map supabase_sub → internal id, with email-based account linking
						var internalID string
						err := db.QueryRowContext(c.Request.Context(),
							`select id from public.users where supabase_sub = $1::uuid`, extSub,
						).Scan(&internalID)
						if errors.Is(err, sql.ErrNoRows) && email != "" {
							err = db.QueryRowContext(c.Request.Context(),
								`update public.users set supabase_sub = $1::uuid where email = $2 returning id`,
								extSub, email,
							).Scan(&internalID)
						}
						if errors.Is(err, sql.ErrNoRows) {
							name := deriveName(email)
							err = db.QueryRowContext(c.Request.Context(), `
                insert into public.users (supabase_sub, email, name)
                values ($1::uuid, $2, $3)
                returning id
              `, extSub, email, name).Scan(&internalID)
						}
						if err == nil && internalID != "" {
							c.Set("uid", internalID)
							if email != "" {
								c.Set("email", email)
							}
							c.Next()
							return
						}
					}
				}
			}
		}

		// 3) 都沒過 → 401
		c.AbortWithStatus(http.StatusUnauthorized)
	}
}

func tryAuth(db *sql.DB) gin.HandlerFunc {
	hmacSecret := []byte(os.Getenv("SESSION_JWT_SECRET"))

	return func(c *gin.Context) {
		// Cookie（已是 internal UUID）
		if cookie, err := c.Cookie("hora_session"); err == nil && cookie != "" {
			if tok, err := jwt.Parse(cookie, func(t *jwt.Token) (any, error) { return hmacSecret, nil }); err == nil && tok.Valid {
				if claims, ok := tok.Claims.(jwt.MapClaims); ok {
					if sub, _ := claims["sub"].(string); sub != "" {
						c.Set("uid", sub)
						if email, _ := claims["email"].(string); email != "" {
							c.Set("email", email)
						}
					}
				}
			}
		}

		// Bearer（需要映射）
		if c.GetString("uid") == "" {
			authz := c.GetHeader("Authorization")
			if strings.HasPrefix(authz, "Bearer ") {
				raw := strings.TrimSpace(authz[7:])
				if tok, err := jwt.Parse(raw, jwks.Keyfunc); err == nil && tok.Valid {
					if claims, ok := tok.Claims.(jwt.MapClaims); ok {
						if extSub, _ := claims["sub"].(string); extSub != "" {
							email, _ := claims["email"].(string)

							// Map supabase_sub → internal id, with email-based account linking
							var internalID string
							err := db.QueryRowContext(c.Request.Context(),
								`select id from public.users where supabase_sub = $1::uuid`, extSub,
							).Scan(&internalID)
							if errors.Is(err, sql.ErrNoRows) && email != "" {
								err = db.QueryRowContext(c.Request.Context(),
									`update public.users set supabase_sub = $1::uuid where email = $2 returning id`,
									extSub, email,
								).Scan(&internalID)
							}
							if errors.Is(err, sql.ErrNoRows) {
								name := deriveName(email)
								err = db.QueryRowContext(c.Request.Context(), `
                  insert into public.users (supabase_sub, email, name)
                  values ($1::uuid, $2, $3)
                  returning id
                `, extSub, email, name).Scan(&internalID)
							}
							if err == nil && internalID != "" {
								c.Set("uid", internalID)
								if email != "" {
									c.Set("email", email)
								}
							}
						}
					}
				}
			}
		}
		c.Next()
	}
}
