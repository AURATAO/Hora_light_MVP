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

		if status != "" && status != "all" {
			w = append(w, "status = $1")
			args = append(args, status)
		}
		if q != "" {
			like := "%" + q + "%"
			if len(args) == 0 {
				w = append(w, "(title ilike $1 or location_text ilike $1 or requester_email ilike $1 or supporter_email ilike $1)")
				args = append(args, like)
			} else {
				w = append(w, fmt.Sprintf(
					"(title ilike $%d or location_text ilike $%d or requester_email ilike $%d or supporter_email ilike $%d)",
					len(args)+1, len(args)+1, len(args)+1, len(args)+1,
				))
				args = append(args, like)
			}
		}

		sqlStr := fmt.Sprintf(`
      select
        task_id, title, category, location_text, status, estimated_minutes,
        prepay_amount, is_immediate, scheduled_at, created_at, cancelled_at, cancel_reason,
        requester_email, supporter_email,
        first_start_at, last_end_at, total_minutes_done, running_minutes,
        (coalesce(total_minutes_done,0)+coalesce(running_minutes,0)) as duration_minutes,
        last_event_at
      from public.view_ops_tasks
      where %s
      order by last_event_at desc
      limit 500
    `, strings.Join(w, " and "))

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
}
