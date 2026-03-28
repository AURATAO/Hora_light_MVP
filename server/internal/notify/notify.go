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
	Type      string // ORDER_ACCEPTED / CLOCK_IN / CLOCK_OUT / CANCELLED / COMPLETED / NEW_MESSAGE
	Title     string
	Body      string
	SendEmail bool
	EmailTo   string

	// Optional extra fields for richer emails
	SupporterName  string
	TaskTitle      string
	ClockInTime    string
	SessionTime    string
	TotalLogged    string
	EstimatedCost  string
	FinalCost      string
	SenderName     string
	MessagePreview string
}

func Create(ctx context.Context, in CreateNotificationInput) error {
	log.Printf("[notify] type=%s user=%s task=%s sendEmail=%v emailTo=%s",
		in.Type, in.UserID, in.TaskID, in.SendEmail, in.EmailTo)

	var emailSentAt *time.Time

	if in.SendEmail && in.EmailTo != "" {
		baseURL := getenv("APP_BASE_URL", "https://horaapp.co")
		taskURL := fmt.Sprintf("%s/tasks/%s", baseURL, in.TaskID)

		html := buildEmail(in, taskURL)

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
    `, in.UserID, in.TaskID, in.Type, in.Title, in.Body, in.SendEmail, emailSentAt)

	return err
}

// buildEmail picks the right branded template based on notification type
func buildEmail(in CreateNotificationInput, taskURL string) string {
	switch in.Type {
	case "CLOCK_IN":
		return clockInEmail(in, taskURL)
	case "CLOCK_OUT":
		return clockOutEmail(in, taskURL)
	case "COMPLETED":
		return taskCompletedEmail(in, taskURL)
	case "NEW_MESSAGE":
		return newMessageEmail(in, taskURL)
	default:
		return defaultEmail(in, taskURL)
	}
}

const emailWrapper = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1.0"/>
<title>%s</title>
</head>
<body style="margin:0;padding:0;background-color:#f4f4f0;font-family:Georgia,'Times New Roman',serif;">
<div style="padding:40px 20px;">
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td align="center">
<table width="100%%" cellpadding="0" cellspacing="0" style="max-width:560px;">

  <!-- Logo -->
  <tr><td align="center" style="padding-bottom:28px;">
    <a href="https://horaapp.co">
      <img src="https://akxsdkerudurzcemurrb.supabase.co/storage/v1/object/public/HORA%%20LOGO/HORALOGO.svg"
           alt="HORA" width="90" style="display:block;height:auto;"/>
    </a>
  </td></tr>

  <!-- Card -->
  <tr><td style="background:#ffffff;border-radius:12px;padding:40px 36px;">
    %s
  </td></tr>

  <!-- Footer -->
  <tr><td style="padding:28px 0 8px;" align="center">
    <a href="https://instagram.com/horaapp" style="display:inline-block;margin-bottom:16px;">
      <img src="https://upload.wikimedia.org/wikipedia/commons/a/a5/Instagram_icon.png"
           alt="Instagram" width="20" height="20" style="display:block;border-radius:5px;margin:0 auto;"/>
    </a>
    <p style="margin:0 0 6px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#9a9a8a;">
      <a href="https://horaapp.co" style="color:#9a9a8a;text-decoration:underline;">horaapp.co</a>
    </p>
    <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;color:#b8b8aa;">© 2026 HORA. All rights reserved.</p>
  </td></tr>

</table>
</td></tr>
</table>
</div>
</body>
</html>`

func wrapEmail(title, cardHTML string) string {
	return fmt.Sprintf(emailWrapper, title, cardHTML)
}

func ctaButton(label, url string) string {
	return fmt.Sprintf(`
<table cellpadding="0" cellspacing="0">
  <tr>
    <td style="border-radius:8px;background:#1a1a16;">
      <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">
        %s
      </a>
    </td>
  </tr>
</table>`, url, label)
}

func pill(color, textColor, label string) string {
	return fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr>
    <td style="background:%s;border-radius:20px;padding:5px 14px;">
      <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:%s;letter-spacing:0.06em;">%s</span>
    </td>
  </tr>
</table>`, color, textColor, label)
}

func detailsTable(rows [][2]string) string {
	var out string
	for _, r := range rows {
		out += fmt.Sprintf(`
<tr>
  <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">%s</td>
  <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
</tr>`, r[0], r[1])
	}
	return fmt.Sprintf(`
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">%s</table>
</div>`, out)
}

// ── Clock In ──────────────────────────────────────────────────────────────────

func clockInEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := in.SupporterName
	if supporterName == "" {
		supporterName = "Your supporter"
	}
	taskTitle := in.TaskTitle
	if taskTitle == "" {
		taskTitle = "your task"
	}
	clockInTime := in.ClockInTime
	if clockInTime == "" {
		clockInTime = time.Now().Format("15:04")
	}

	card := pill("#e8f5e9", "#2e7d32", "● CLOCKED IN") +
		fmt.Sprintf(`<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s has started working</h1>`, supporterName) +
		`<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">Your supporter just clocked in. The timer is now running.</p>` +
		detailsTable([][2]string{
			{"Task", taskTitle},
			{"Supporter", supporterName},
			{"Clocked in at", clockInTime},
		}) +
		ctaButton("View task →", taskURL)

	return wrapEmail("Supporter clocked in", card)
}

// ── Clock Out ─────────────────────────────────────────────────────────────────

func clockOutEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := in.SupporterName
	if supporterName == "" {
		supporterName = "Your supporter"
	}
	taskTitle := in.TaskTitle
	if taskTitle == "" {
		taskTitle = "your task"
	}

	card := pill("#fff3e0", "#e65100", "● CLOCKED OUT") +
		fmt.Sprintf(`<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s has clocked out</h1>`, supporterName) +
		`<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">Here's a summary of this session.</p>` +
		detailsTable([][2]string{
			{"Task", taskTitle},
			{"Supporter", supporterName},
			{"Session time", in.SessionTime},
			{"Total logged", in.TotalLogged},
			{"Est. cost so far", in.EstimatedCost},
		}) +
		ctaButton("View task →", taskURL)

	return wrapEmail("Supporter clocked out", card)
}

// ── Task Completed ────────────────────────────────────────────────────────────

func taskCompletedEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := in.SupporterName
	if supporterName == "" {
		supporterName = "Your supporter"
	}
	taskTitle := in.TaskTitle
	if taskTitle == "" {
		taskTitle = "your task"
	}
	reviewURL := fmt.Sprintf("%s?review=true", taskURL)

	card := pill("#e8f5e9", "#2e7d32", "✓ TASK COMPLETE") +
		`<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">Your task has been completed</h1>` +
		fmt.Sprintf(`<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">%s has marked your task as complete. Here's the final summary.</p>`, supporterName) +
		detailsTable([][2]string{
			{"Task", taskTitle},
			{"Supporter", supporterName},
			{"Total time", in.TotalLogged},
			{"Final cost", in.FinalCost},
		}) +
		fmt.Sprintf(`<div style="border-left:3px solid #1a1a16;padding-left:16px;margin-bottom:28px;">
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:1.6;color:#555550;">
    Happy with the work? Leave %s a review — it helps them get more tasks on HORA.
  </p>
</div>`, supporterName) +
		fmt.Sprintf(`<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;padding-right:12px;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task →</a>
  </td>
  <td style="border-radius:8px;border:1px solid #1a1a16;">
    <a href="%s" style="display:inline-block;padding:13px 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#1a1a16;text-decoration:none;border-radius:8px;">Leave a review</a>
  </td>
</tr></table>`, taskURL, reviewURL)

	return wrapEmail("Task completed", card)
}

// ── New Message ───────────────────────────────────────────────────────────────

func newMessageEmail(in CreateNotificationInput, taskURL string) string {
	senderName := in.SenderName
	if senderName == "" {
		senderName = "Someone"
	}
	taskTitle := in.TaskTitle
	if taskTitle == "" {
		taskTitle = "your task"
	}
	messagePreview := in.MessagePreview
	if messagePreview == "" {
		messagePreview = in.Body
	}

	card := `<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">New message</p>` +
		fmt.Sprintf(`<h1 style="margin:0 0 20px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s sent you a message</h1>`, senderName) +
		fmt.Sprintf(`<div style="background:#f4f4f0;border-radius:4px 12px 12px 12px;padding:16px 20px;margin-bottom:24px;border-left:3px solid #1a1a16;">
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:15px;line-height:1.65;color:#1a1a16;font-style:italic;">"%s"</p>
</div>`, messagePreview) +
		fmt.Sprintf(`<div style="margin-bottom:28px;">
  <p style="margin:0 0 4px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;color:#9a9a8a;text-transform:uppercase;letter-spacing:0.08em;">Re: task</p>
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;color:#1a1a16;font-weight:500;">%s</p>
</div>`, taskTitle) +
		ctaButton("Reply →", taskURL)

	return wrapEmail("New message on HORA", card)
}

// ── Default (ORDER_ACCEPTED, CANCELLED, etc.) ─────────────────────────────────

func defaultEmail(in CreateNotificationInput, taskURL string) string {
	card := fmt.Sprintf(`
<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">Notification</p>
<h1 style="margin:0 0 16px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s</h1>
<p style="margin:0 0 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">%s</p>`,
		in.Title, in.Body) +
		ctaButton("View task →", taskURL)

	return wrapEmail(in.Title, card)
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
