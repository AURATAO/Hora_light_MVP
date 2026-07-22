package ops

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RegisterOpsRoutes 由 main 注入：auth 中介層 + admin 檢查函式
func RegisterOpsRoutes(r *gin.Engine, sqldb *sql.DB, authMW gin.HandlerFunc, isAdmin func(email string) bool) {
	ops := r.Group("/ops")
	ops.Use(authMW)

	// GET /ops/feed?status=all&q=

	ops.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })
	ops.GET("/feed", func(c *gin.Context) {
		email := c.GetString("email")
		if !isAdmin(email) {
			c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
			return
		}
		status := strings.TrimSpace(c.Query("status"))
		if status == "" {
			status = "all"
		}
		q := strings.TrimSpace(c.Query("q"))

		var args []any
		w := []string{"1=1"}
		next := 1

		switch status {
		case "", "all":
			// no-op
		case "open":
			w = append(w, "status = 'open' AND supporter_email IS NULL")
		case "accepted":
			w = append(w, "status = 'open' AND supporter_email IS NOT NULL AND COALESCE(running_minutes,0)=0 AND COALESCE(total_minutes_done,0)=0")
		case "in_progress":
			w = append(w, "status = 'open' AND (COALESCE(running_minutes,0) > 0 OR COALESCE(total_minutes_done,0) > 0)")
		case "completed":
			w = append(w, "status = 'completed'")
		case "cancelled":
			w = append(w, "status = 'cancelled'")
		default:
			// 不認得的值 → 當 all
		}
		if q != "" {
			like := "%" + q + "%"
			w = append(w, fmt.Sprintf(
				"(title ILIKE $%[1]d OR location_text ILIKE $%[1]d OR requester_email ILIKE $%[1]d OR supporter_email ILIKE $%[1]d)",
				next,
			))
			args = append(args, like)
			next++
		}

		sqlStr := fmt.Sprintf(`
			SELECT
				task_id, title, category, location_text, status, estimated_minutes,
				prepay_amount, is_immediate, scheduled_at, created_at, cancelled_at, cancel_reason,
				requester_email, supporter_email,
				first_start_at, last_end_at, total_minutes_done, running_minutes,
				(COALESCE(total_minutes_done,0)+COALESCE(running_minutes,0)) AS duration_minutes,
				last_event_at
			FROM public.view_ops_tasks
			WHERE %s
			ORDER BY last_event_at DESC
			LIMIT 500
			`, strings.Join(w, " AND "))

		rows, err := sqldb.QueryContext(c.Request.Context(), sqlStr, args...)
		if err != nil {
			log.Printf("[ops.feed][query] %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		defer rows.Close()

		type Row struct {
			TaskID           string     `json:"task_id"`
			Title            string     `json:"title"`
			Category         string     `json:"category"`
			LocationText     string     `json:"location_text"`
			Status           string     `json:"status"`
			EstimatedMinutes int        `json:"estimated_minutes"`
			PrepayAmount     *float64   `json:"prepay_amount"`
			IsImmediate      bool       `json:"is_immediate"`
			ScheduledAt      *time.Time `json:"scheduled_at"`
			CreatedAt        time.Time  `json:"created_at"`
			CancelledAt      *time.Time `json:"cancelled_at"`
			CancelReason     *string    `json:"cancel_reason"`
			RequesterEmail   *string    `json:"requester_email"`
			SupporterEmail   *string    `json:"supporter_email"`
			FirstStartAt     *time.Time `json:"first_start_at"`
			LastEndAt        *time.Time `json:"last_end_at"`
			TotalDone        *int       `json:"total_minutes_done"`
			Running          *int       `json:"running_minutes"`
			Duration         *int       `json:"duration_minutes"`
			LastEventAt      time.Time  `json:"last_event_at"`
		}

		out := []Row{}
		for rows.Next() {
			var r Row
			if err := rows.Scan(
				&r.TaskID, &r.Title, &r.Category, &r.LocationText, &r.Status, &r.EstimatedMinutes,
				&r.PrepayAmount, &r.IsImmediate, &r.ScheduledAt, &r.CreatedAt, &r.CancelledAt, &r.CancelReason,
				&r.RequesterEmail, &r.SupporterEmail,
				&r.FirstStartAt, &r.LastEndAt, &r.TotalDone, &r.Running, &r.Duration, &r.LastEventAt,
			); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
				return
			}
			out = append(out, r)
		}
		c.JSON(http.StatusOK, out)
	})

	// GET /ops/supporter-applications
	//
	// Every profile that has ever applied, newest application first — pending
	// and already-decided alike, so the ops panel can render the queue and a
	// decision log from one call. The client groups them; the API does not
	// pre-filter, because "pending" is derived from three columns and that
	// derivation belongs in one place (S-05: read supporter_status, not the
	// raw timestamps, when you only need the state).
	ops.GET("/supporter-applications", func(c *gin.Context) {
		if !isAdmin(c.GetString("email")) {
			c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
			return
		}

		rows, err := sqldb.QueryContext(c.Request.Context(), `
			SELECT id::text, coalesce(email,''), coalesce(name,''), coalesce(phone,''), coalesce(city,''),
			       supporter_applied_at, supporter_rejected_at, coalesce(is_verified_supporter,false)
			FROM public.profiles
			WHERE supporter_applied_at IS NOT NULL
			ORDER BY supporter_applied_at DESC
			LIMIT 500
		`)
		if err != nil {
			log.Printf("[ops.supporter-applications][query] %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		defer rows.Close()

		// Phone is PII (S-12): it travels to the admin panel because reviewing an
		// application needs it, but it is never logged here or anywhere below.
		type Applicant struct {
			ID                  string     `json:"id"`
			Email               string     `json:"email"`
			Name                string     `json:"name"`
			Phone               string     `json:"phone"`
			City                string     `json:"city"`
			SupporterAppliedAt  *time.Time `json:"supporter_applied_at"`
			SupporterRejectedAt *time.Time `json:"supporter_rejected_at"`
			IsVerifiedSupporter bool       `json:"is_verified_supporter"`
			// Same derivation as GET /profile's supporter_status (server/main.go
			// deriveSupporterStatus) — kept server-side so the panel never
			// re-implements it (S-05).
			SupporterStatus string `json:"supporter_status"`
		}

		out := []Applicant{}
		for rows.Next() {
			var a Applicant
			if err := rows.Scan(
				&a.ID, &a.Email, &a.Name, &a.Phone, &a.City,
				&a.SupporterAppliedAt, &a.SupporterRejectedAt, &a.IsVerifiedSupporter,
			); err != nil {
				log.Printf("[ops.supporter-applications][scan] %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
				return
			}
			switch {
			case a.IsVerifiedSupporter:
				a.SupporterStatus = "approved"
			case a.SupporterRejectedAt != nil:
				a.SupporterStatus = "rejected"
			default:
				a.SupporterStatus = "applied"
			}
			out = append(out, a)
		}
		if err := rows.Err(); err != nil {
			log.Printf("[ops.supporter-applications][rows] %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, out)
	})

	// POST /ops/force-complete  { "task_id": "uuid" }
	ops.POST("/force-complete", func(c *gin.Context) {
		email := c.GetString("email")
		if !isAdmin(email) {
			c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
			return
		}
		var in struct {
			TaskID string `json:"task_id"`
		}
		if err := c.BindJSON(&in); err != nil || strings.TrimSpace(in.TaskID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
			return
		}
		if _, err := sqldb.ExecContext(c.Request.Context(),
			`select public.force_complete($1::uuid)`, in.TaskID,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// POST /ops/cancel  { "task_id": "uuid", "reason": "text" }
	ops.POST("/cancel", func(c *gin.Context) {
		email := c.GetString("email")
		if !isAdmin(email) {
			c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
			return
		}
		var in struct {
			TaskID string `json:"task_id"`
			Reason string `json:"reason"`
		}
		if err := c.BindJSON(&in); err != nil || strings.TrimSpace(in.TaskID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
			return
		}
		if _, err := sqldb.ExecContext(c.Request.Context(),
			`select public.cancel_task($1::uuid, $2)`, in.TaskID, in.Reason,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// POST /ops/adjust-time  { "task_id": "uuid", "delta": 5 }
	ops.POST("/adjust-time", func(c *gin.Context) {
		email := c.GetString("email")
		if !isAdmin(email) {
			c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
			return
		}
		var in struct {
			TaskID string `json:"task_id"`
			Delta  int    `json:"delta"`
		}
		if err := c.BindJSON(&in); err != nil || strings.TrimSpace(in.TaskID) == "" || in.Delta == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
			return
		}
		if _, err := sqldb.ExecContext(c.Request.Context(),
			`select public.adjust_time($1::uuid, $2)`, in.TaskID, in.Delta,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// Supporter application decisions. Both were previously manual dashboard
	// edits to public.profiles; these give the ops surface the same two writes
	// behind the admin allowlist, so supporter_status stays a pure derivation
	// of columns the API owns (S-05).
	//
	// Identify the profile by either key; profile_id wins when both are sent.

	// POST /ops/supporter-approve  { "profile_id": "uuid" } | { "email": "..." }
	ops.POST("/supporter-approve", func(c *gin.Context) {
		supporterDecision(c, sqldb, isAdmin,
			`UPDATE public.profiles
			    SET is_verified_supporter = true,
			        supporter_rejected_at = NULL,
			        updated_at = now()`)
	})

	// POST /ops/supporter-reject  { "profile_id": "uuid" } | { "email": "..." }
	ops.POST("/supporter-reject", func(c *gin.Context) {
		supporterDecision(c, sqldb, isAdmin,
			`UPDATE public.profiles
			    SET supporter_rejected_at = now(),
			        is_verified_supporter = false,
			        updated_at = now()`)
	})
}

// supporterDecision runs an admin-only UPDATE on one profile row, selected by
// profile_id or email. `setClause` is a trusted constant from this file — the
// only interpolation — and the identifier is always a bound parameter.
func supporterDecision(c *gin.Context, sqldb *sql.DB, isAdmin func(email string) bool, setClause string) {
	if !isAdmin(c.GetString("email")) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not authorized"})
		return
	}
	var in struct {
		ProfileID string `json:"profile_id"`
		Email     string `json:"email"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	profileID := strings.TrimSpace(in.ProfileID)
	target := strings.TrimSpace(in.Email)
	if profileID == "" && target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id or email required"})
		return
	}

	where := ` WHERE email = $1`
	arg := any(target)
	if profileID != "" {
		where = ` WHERE id = $1::uuid`
		arg = any(profileID)
	}

	res, err := sqldb.ExecContext(c.Request.Context(), setClause+where, arg)
	if err != nil {
		log.Printf("[ops/supporter-decision] db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if n, errRows := res.RowsAffected(); errRows == nil && n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
