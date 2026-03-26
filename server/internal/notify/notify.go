package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type EmailPayload struct {
	To      string
	Subject string
	Html    string
}

func SendEmail(p EmailPayload) error {
	token := os.Getenv("POSTMARK_API_TOKEN")
	from := os.Getenv("EMAIL_FROM")
	if token == "" {
		return fmt.Errorf("POSTMARK_API_TOKEN not set")
	}
	if from == "" {
		from = "Ho:ra <no-reply@horaapp.co>"
	}

	log.Printf("[email] postmark to=%s subject=%s", p.To, p.Subject)

	payload := map[string]string{
		"From":     from,
		"To":       p.To,
		"Subject":  p.Subject,
		"HtmlBody": p.Html,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.postmarkapp.com/email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Postmark-Server-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[email] postmark error status=%d body=%s", resp.StatusCode, string(respBody))
		return fmt.Errorf("postmark returned %d", resp.StatusCode)
	}
	return nil
}

type CreateNotificationInput struct {
	DB        *sql.DB
	UserID    string
	TaskID    string
	Type      string // ORDER_ACCEPTED / CLOCK_IN / CLOCK_OUT / CANCELLED / COMPLETED
	Title     string
	Body      string // 純文字或已轉好的 HTML 片段
	SendEmail bool
	EmailTo   string // 有 email 再寄
}

func Create(ctx context.Context, in CreateNotificationInput) error {
	log.Printf("[notify] type=%s user=%s task=%s sendEmail=%v emailTo=%s",
		in.Type, in.UserID, in.TaskID, in.SendEmail, in.EmailTo)

	var emailSentAt *time.Time

	if in.SendEmail && in.EmailTo != "" {
		html := fmt.Sprintf(
			`<div style="font-family:Inter,system-ui">
		<h2>%s</h2>
		<p>%s</p>
		<p style="margin-top:16px"><a href="%s/tasks/%s">View Task</a></p>
		</div>`,
			in.Title, in.Body, getenv("APP_BASE_URL", "https://my-hora.com"), in.TaskID,
		)
		if err := SendEmail(EmailPayload{To: in.EmailTo, Subject: in.Title, Html: html}); err != nil {
			log.Printf("SendEmail error: %v", err)
		} else {
			t := nowRome()
			emailSentAt = &t
		}
	}

	// ← DB 這邊以前就已經是 task_id 欄位，參數也要改用 in.TaskID
	_, err := in.DB.ExecContext(ctx, `
        INSERT INTO notifications (user_id, task_id, type, title, body, unread, via_email, email_sent_at)
        VALUES ($1,$2,$3,$4,$5,true,$6,$7)
    `, in.UserID, in.TaskID, in.Type, in.Title, in.Body, in.SendEmail, emailSentAt)

	return err
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func nowRome() time.Time {
	loc, _ := time.LoadLocation(os.Getenv("TZ"))
	if loc == nil {
		loc = time.Local
	}
	return time.Now().In(loc)
}
