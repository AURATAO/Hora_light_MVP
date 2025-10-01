package notify

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

type EmailPayload struct {
	To      string
	Subject string
	Html    string
}

func SendEmail(p EmailPayload) error {
	log.Printf("[email] dialing host=%s port=%s to=%s",
		os.Getenv("SMTP_HOST"), os.Getenv("SMTP_PORT"), p.To)

	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	from := os.Getenv("EMAIL_FROM")

	if host == "" || port == "" || user == "" || pass == "" || from == "" {
		return fmt.Errorf("SMTP not configured")
	}

	addr := net.JoinHostPort(host, port)
	auth := smtp.PlainAuth("", user, pass, host)

	headers := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", p.To),
		fmt.Sprintf("Subject: %s", p.Subject),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
	}
	if rt := os.Getenv("REPLY_TO"); rt != "" {
		headers = append(headers, fmt.Sprintf("Reply-To: %s", rt))
	}
	msg := strings.Join(append(headers, "", p.Html), "\r\n")

	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		cfg := &tls.Config{ServerName: host}
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
	}
	if err := c.Auth(auth); err != nil {
		return err
	}

	fromAddr := strings.TrimSpace(from)
	if i := strings.LastIndex(fromAddr, "<"); i >= 0 {
		fromAddr = strings.Trim(fromAddr[i+1:], ">")
	}
	if err := c.Mail(fromAddr); err != nil {
		return err
	}
	if err := c.Rcpt(p.To); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		return err
	}
	return wc.Close()
}

type CreateNotificationInput struct {
	DB        *sql.DB
	UserID    string
	JobID     string
	Type      string // ORDER_ACCEPTED / CLOCK_IN / CLOCK_OUT / CANCELLED / COMPLETED
	Title     string
	Body      string // 純文字或已轉好的 HTML 片段
	SendEmail bool
	EmailTo   string // 有 email 再寄
}

func Create(ctx context.Context, in CreateNotificationInput) error {
	log.Printf("[notify] type=%s user=%s job=%s sendEmail=%v emailTo=%s", in.Type, in.UserID, in.JobID, in.SendEmail, in.EmailTo)

	var emailSentAt *time.Time

	if in.SendEmail && in.EmailTo != "" {
		html := fmt.Sprintf(`<div style="font-family:Inter,system-ui">
<h2>%s</h2>
<p>%s</p>
<p style="margin-top:16px"><a href="%s/jobs/%s">View Job</a></p>
</div>`, in.Title, in.Body, getenv("APP_BASE_URL", "https://my-hora.com"), in.JobID)

		if err := SendEmail(EmailPayload{To: in.EmailTo, Subject: in.Title, Html: html}); err != nil {
			log.Printf("SendEmail error: %v", err)
		} else {
			t := nowRome()
			emailSentAt = &t
		}
	}

	_, err := in.DB.ExecContext(ctx, `
		INSERT INTO notifications (user_id, task_id, type, title, body, unread, via_email, email_sent_at)
		VALUES ($1,$2,$3,$4,$5,true,$6,$7)
	`, in.UserID, in.JobID, in.Type, in.Title, in.Body, in.SendEmail, emailSentAt)

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
