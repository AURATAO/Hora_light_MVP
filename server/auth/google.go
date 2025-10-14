package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ====== 公用型別 ======

type Claims struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
	jwt.RegisteredClaims
}

// ====== 這裡持有 verifier / oauthCfg 與 DB ======

var (
	verifier *oidc.IDTokenVerifier
	oauthCfg *oauth2.Config

	// 供 HandleCallback 查/建使用者用
	DB *sql.DB
)

// 讓 main.go 在初始化好 sqldb 後注入
func SetDB(db *sql.DB) { DB = db }

// ====== 初始化 Google OAuth ======

func InitGoogleOAuth() error {
	ctx := context.Background()

	clientID := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET"))
	redirectURL := strings.TrimSpace(os.Getenv("OAUTH_REDIRECT_URL"))
	if redirectURL == "" {
		redirectURL = "http://localhost:8080/auth/callback"
	}
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("missing GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET")
	}

	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return err
	}

	oauthCfg = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
	verifier = provider.Verifier(&oidc.Config{ClientID: clientID})

	log.Printf("[oauth] client_id=%s redirect=%s", clientID, redirectURL)
	return nil
}

func randomState() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ====== /auth/login ======

func HandleLogin(c *gin.Context) {
	state, _ := randomState()

	// 解析 next（可相對路徑），補成絕對 URL
	next := c.Query("next")
	if next == "" {
		if base := strings.TrimSpace(os.Getenv("APP_BASE_URL")); base != "" {
			next = base
		} else {
			next = "http://localhost:5173"
		}
	}
	if strings.HasPrefix(next, "/") {
		base := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
		if base == "" {
			base = "http://localhost:5173"
		}
		next = strings.TrimRight(base, "/") + next
	}

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // prod 設 true + SameSite=None
		MaxAge:   300,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "oauth_next",
		Value:    next,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		MaxAge:   600,
	})
	log.Printf("[oauth] set state=%s next=%s", state, next)

	url := oauthCfg.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("redirect_uri", oauthCfg.RedirectURL),
	)
	c.Redirect(http.StatusFound, url)
}

// ====== /auth/callback ======

func HandleCallback(c *gin.Context) {
	want, _ := c.Cookie("oauth_state")
	got := c.Query("state")
	log.Printf("[oauth] cb state got=%s want=%s", got, want)
	if want == "" || got != want {
		c.String(http.StatusBadRequest, "invalid state")
		return
	}
	code := c.Query("code")
	if code == "" {
		c.String(http.StatusBadRequest, "missing code")
		return
	}

	// 1) 換 token / 取 id_token / 驗證
	ctx := context.Background()
	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		c.String(http.StatusUnauthorized, "exchange failed")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		c.String(http.StatusUnauthorized, "no id_token in token response")
		return
	}
	idt, err := verifier.Verify(ctx, rawID)
	if err != nil {
		c.String(http.StatusUnauthorized, "verify failed")
		return
	}

	// 2) 解析 Google claims
	var p struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	_ = idt.Claims(&p)
	googleSub := idt.Subject // ex: "116489687343525085686"

	// 3) 查或建 internal user，取得 internal UUID
	if DB == nil {
		c.String(http.StatusInternalServerError, "DB not set")
		return
	}
	var internalID string
	err = DB.QueryRowContext(ctx,
		`SELECT id FROM public.users WHERE google_sub = $1`,
		googleSub,
	).Scan(&internalID)

	if errors.Is(err, sql.ErrNoRows) {
		err = DB.QueryRowContext(ctx, `
			INSERT INTO public.users (google_sub, email, name, picture)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, googleSub, p.Email, p.Name, p.Picture).Scan(&internalID)
	}
	if err != nil || internalID == "" {
		log.Printf("[oauth] upsert user err=%v", err)
		c.String(http.StatusInternalServerError, "user upsert failed")
		return
	}

	// 4) 簽發我們自己的 session（sub=internal UUID）
	ttl := 24 * time.Hour
	now := time.Now()
	j := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Email: p.Email, Name: p.Name, Picture: p.Picture,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   internalID, // <<<< 關鍵：用 internal UUID
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	})
	signed, err := j.SignedString([]byte(os.Getenv("SESSION_JWT_SECRET")))
	if err != nil {
		c.String(http.StatusInternalServerError, "session error")
		return
	}

	// 5) 讀 next（支援相對 → 絕對）
	next := "http://localhost:5173"
	if v, err := c.Cookie("oauth_next"); err == nil && v != "" {
		next = v
	}
	if strings.HasPrefix(next, "/") {
		base := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
		if base == "" {
			base = "http://localhost:5173"
		}
		next = strings.TrimRight(base, "/") + next
	}

	// 6) 設置 cookie
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "hora_session",
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // prod 設 true + SameSite=None + Domain
		MaxAge:   int(ttl.Seconds()),
	})

	// 7) 清除一次性 cookie
	http.SetCookie(c.Writer, &http.Cookie{Name: "oauth_state", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.SetCookie(c.Writer, &http.Cookie{Name: "oauth_next", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})

	// 8) 導回前端
	c.Redirect(http.StatusFound, next)
}
