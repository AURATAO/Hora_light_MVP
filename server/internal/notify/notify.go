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

	log.Printf("[email] postmark sending to=%s subject=%q", p.To, p.Subject)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[email] postmark request error to=%s: %v", p.To, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[email] postmark error to=%s status=%d body=%s", p.To, resp.StatusCode, string(respBody))
		return fmt.Errorf("postmark returned %d", resp.StatusCode)
	}
	log.Printf("[email] postmark ok to=%s subject=%q status=200", p.To, p.Subject)
	return nil
}

type CreateNotificationInput struct {
	DB             *sql.DB
	UserID         string
	TaskID         string
	Type           string
	Title          string
	Body           string
	SendEmail      bool
	EmailTo        string
	SupporterName  string
	TaskTitle      string
	ClockInTime    string
	SessionTime    string
	TotalLogged    string
	EstimatedCost  string
	FinalCost      string
	SenderName         string
	MessagePreview     string
	CompletionPhotoURL string
	CompletionNote     string
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
			log.Printf("[email] SEND FAILED to=%s type=%s err=%v", in.EmailTo, in.Type, err)
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

func buildEmail(in CreateNotificationInput, taskURL string) string {
	switch in.Type {
	case "ORDER_ACCEPTED":
		return orderAcceptedEmail(in, taskURL)
	case "CLOCK_IN":
		return clockInEmail(in, taskURL)
	case "CLOCK_OUT":
		return clockOutEmail(in, taskURL)
	case "COMPLETED":
		return taskCompletedEmail(in, taskURL)
	case "COMPLETED_SUPPORTER":
		return taskCompletedSupporterEmail(in, taskURL)
	case "CANCELLED":
		return taskCancelledEmail(in, taskURL)
	case "NEW_MESSAGE":
		return newMessageEmail(in, taskURL)
	default:
		return defaultEmail(in, taskURL)
	}
}

func wrapEmail(title, cardHTML string) string {
	header := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1.0"/>
<title>` + title + `</title>
<style>
body{margin:0;padding:0;background-color:#f4f4f0;font-family:Georgia,'Times New Roman',serif;}
a{color:inherit;text-decoration:none;}
@media(max-width:600px){.wrapper{padding:24px 16px!important}.card{padding:32px 24px!important}}
</style>
</head>
<body>
<div class="wrapper" style="background-color:#f4f4f0;padding:40px 20px;">
<table width="100%" cellpadding="0" cellspacing="0" role="presentation">
<tr><td align="center">
<table width="100%" cellpadding="0" cellspacing="0" role="presentation" style="max-width:560px;">
<tr><td align="center" style="padding-bottom:28px;">
  <a href="https://horaapp.co">
    <img src="https://akxsdkerudurzcemurrb.supabase.co/storage/v1/object/public/HORA%20LOGO/HO_RA%20(2).png"
         alt="HORA" width="90" style="display:block;height:auto;"/>
  </a>
</td></tr>
<tr><td class="card" style="background:#ffffff;border-radius:12px;padding:40px 36px;">
`

	footer := `
</td></tr>
<tr><td style="padding:28px 0 8px;" align="center">
  <a href="https://www.instagram.com/my_hora_app/" style="display:inline-block;margin-bottom:16px;"> <img src="https://akxsdkerudurzcemurrb.supabase.co/storage/v1/object/public/HORA%20LOGO/iglogo.png" alt="Instagram" width="20" height="20" style="display:block;margin:0 auto;"/> </a>
  <p style="margin:0 0 6px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#9a9a8a;">
    <a href="https://horaapp.co" style="color:#9a9a8a;text-decoration:underline;">horaapp.co</a>
  </p>
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;color:#b8b8aa;">
    &copy; 2026 HORA. All rights reserved.
  </p>
</td></tr>
</table>
</td></tr>
</table>
</div>
</body>
</html>`

	return header + cardHTML + footer
}

func orderAcceptedEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := fallback(in.SupporterName, "A supporter")
	taskTitle := fallback(in.TaskTitle, "your task")
	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#e8f5e9;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#2e7d32;letter-spacing:0.06em;">&#10003; ACCEPTED</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s accepted your task</h1>
<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">Your request has been accepted. You'll be notified as soon as they clock in and the timer starts.</p>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Supporter</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		supporterName, taskTitle, supporterName, taskURL)
	return wrapEmail("Task accepted", card)
}

func taskCancelledEmail(in CreateNotificationInput, taskURL string) string {
	taskTitle := fallback(in.TaskTitle, "your task")
	reason := fallback(in.Body, "No reason provided.")
	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#fce8e8;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#b71c1c;letter-spacing:0.06em;">&#10005; CANCELLED</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s</h1>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Reason</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		in.Title, taskTitle, reason, taskURL)
	return wrapEmail("Task cancelled", card)
}

func taskCompletedSupporterEmail(in CreateNotificationInput, taskURL string) string {
	taskTitle := fallback(in.TaskTitle, "the task")
	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#e8f5e9;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#2e7d32;letter-spacing:0.06em;">&#10003; TASK COMPLETE</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">Great work — task complete!</h1>
<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">The task has been marked as complete. Here's a summary of your session.</p>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Time logged</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Earnings</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:600;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		taskTitle,
		fallback(in.TotalLogged, "—"),
		fallback(in.FinalCost, "—"),
		taskURL)
	return wrapEmail("Task complete — great work!", card)
}

func clockInEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := fallback(in.SupporterName, "Your supporter")
	taskTitle := fallback(in.TaskTitle, "your task")
	clockInTime := fallback(in.ClockInTime, time.Now().Format("15:04"))
	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#e8f5e9;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#2e7d32;letter-spacing:0.06em;">&#9679; CLOCKED IN</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s has started working</h1>
<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">Your supporter just clocked in on your task. The timer is now running.</p>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Supporter</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Clocked in at</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		supporterName, taskTitle, supporterName, clockInTime, taskURL)
	return wrapEmail("Supporter clocked in", card)
}

func clockOutEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := fallback(in.SupporterName, "Your supporter")
	taskTitle := fallback(in.TaskTitle, "your task")
	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#fff3e0;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#e65100;letter-spacing:0.06em;">&#9679; CLOCKED OUT</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s has clocked out</h1>
<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">Here's a summary of this session on your task.</p>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Supporter</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Session time</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Total logged</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Est. cost so far</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		supporterName, taskTitle, supporterName,
		fallback(in.SessionTime, "—"),
		fallback(in.TotalLogged, "—"),
		fallback(in.EstimatedCost, "—"),
		taskURL)
	return wrapEmail("Supporter clocked out", card)
}

func taskCompletedEmail(in CreateNotificationInput, taskURL string) string {
	supporterName := fallback(in.SupporterName, "Your supporter")
	taskTitle := fallback(in.TaskTitle, "your task")
	reviewURL := taskURL + "/review"

	var photoSection string
	if in.CompletionPhotoURL != "" {
		photoSection += `<img src="` + in.CompletionPhotoURL + `" style="width:100%;max-width:500px;border-radius:8px;margin-top:16px;" alt="Completion photo"/>`
	}
	if in.CompletionNote != "" {
		photoSection += `<div style="margin-top:16px;padding:12px 16px;background:#f5f5f0;border-radius:8px;">` +
			`<p style="margin:0;font-size:13px;color:#888;text-transform:uppercase;letter-spacing:0.05em;">Note from your supporter</p>` +
			`<p style="margin:8px 0 0;font-size:15px;color:#333;">` + in.CompletionNote + `</p>` +
			`</div>`
	}

	card := fmt.Sprintf(`
<table cellpadding="0" cellspacing="0" style="margin-bottom:20px;">
  <tr><td style="background:#e8f5e9;border-radius:20px;padding:5px 14px;">
    <span style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;font-weight:600;color:#2e7d32;letter-spacing:0.06em;">&#10003; TASK COMPLETE</span>
  </td></tr>
</table>
<h1 style="margin:0 0 12px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">Your task has been completed</h1>
<p style="margin:0 0 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">%s has marked your task as complete. Here's the final summary.</p>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:24px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:40%%;">Task</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Supporter</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Total time</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Final cost</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:600;">%s</td>
    </tr>
  </table>
</div>
%s<div style="border-left:3px solid #1a1a16;padding-left:16px;margin-bottom:28px;">
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:1.6;color:#555550;">Happy with the work? Leave %s a review &mdash; it helps them get more tasks on HORA.</p>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;padding-right:12px;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
  <td style="border-radius:8px;border:1px solid #1a1a16;">
    <a href="%s" style="display:inline-block;padding:13px 24px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#1a1a16;text-decoration:none;border-radius:8px;">Leave a review</a>
  </td>
</tr></table>`,
		supporterName,
		taskTitle, supporterName,
		fallback(in.TotalLogged, "—"),
		fallback(in.FinalCost, "—"),
		photoSection,
		supporterName,
		taskURL, reviewURL)
	return wrapEmail("Task completed", card)
}

func newMessageEmail(in CreateNotificationInput, taskURL string) string {
	senderName := fallback(in.SenderName, "Someone")
	taskTitle := fallback(in.TaskTitle, "your task")
	messagePreview := fallback(in.MessagePreview, in.Body)
	card := fmt.Sprintf(`
<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">New message</p>
<h1 style="margin:0 0 20px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s sent you a message</h1>
<div style="background:#f4f4f0;border-radius:4px 12px 12px 12px;padding:16px 20px;margin-bottom:24px;border-left:3px solid #1a1a16;">
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:15px;line-height:1.65;color:#1a1a16;font-style:italic;">&ldquo;%s&rdquo;</p>
</div>
<div style="margin-bottom:28px;">
  <p style="margin:0 0 4px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;color:#9a9a8a;text-transform:uppercase;letter-spacing:0.08em;">Re: task</p>
  <p style="margin:0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;color:#1a1a16;font-weight:500;">%s</p>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">Reply &rarr;</a>
  </td>
</tr></table>`,
		senderName, messagePreview, taskTitle, taskURL)
	return wrapEmail("New message on HORA", card)
}

func defaultEmail(in CreateNotificationInput, taskURL string) string {
	card := fmt.Sprintf(`
<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">Notification</p>
<h1 style="margin:0 0 16px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s</h1>
<p style="margin:0 0 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#555550;">%s</p>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		in.Title, in.Body, taskURL)
	return wrapEmail(in.Title, card)
}

type AdminNewTaskInput struct {
	TaskID           string
	Title            string
	Category         string
	RequesterEmail   string
	LocationText     string
	EstimatedMinutes int
	IsImmediate      bool
	ScheduledAt      *time.Time
}

func NotifyAdminNewTask(in AdminNewTaskInput) {
	baseURL := getenv("APP_BASE_URL", "https://horaapp.co")
	taskURL := fmt.Sprintf("%s/tasks/%s", baseURL, in.TaskID)

	when := "ASAP"
	if !in.IsImmediate && in.ScheduledAt != nil {
		when = in.ScheduledAt.Format("2006-01-02 15:04 MST")
	}

	loc := in.LocationText
	if loc == "" {
		loc = "—"
	}

	card := fmt.Sprintf(`
<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">New task posted</p>
<h1 style="margin:0 0 20px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">%s</h1>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:36%%;">Category</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Requester</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Location</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Duration</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%d min</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">When</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;">%s</td>
    </tr>
  </table>
</div>
<table cellpadding="0" cellspacing="0"><tr>
  <td style="border-radius:8px;background:#1a1a16;">
    <a href="%s" style="display:inline-block;padding:14px 28px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;font-weight:500;color:#f4f4f0;text-decoration:none;border-radius:8px;">View task &rarr;</a>
  </td>
</tr></table>`,
		in.Title, in.Category, in.RequesterEmail, loc, in.EstimatedMinutes, when, taskURL)

	html := wrapEmail("New task posted: "+in.Title, card)
	subject := fmt.Sprintf("New task posted: %s", in.Title)

	adminEmails := []string{
		getenv("ADMIN_EMAIL", "liang.you@horaapp.co"),
		"daniele@arcodiax.com",
		"liang.you@arcodiax.com",
	}
	for _, email := range adminEmails {
		if err := SendEmail(EmailPayload{To: email, Subject: subject, Html: html}); err != nil {
			log.Printf("[notify] admin new-task email failed to=%s taskID=%s err=%v", email, in.TaskID, err)
		}
	}
}

type SupporterApplyInput struct {
	FirstName string
	LastName  string
	Phone     string
	City      string
	Email     string
	AppliedAt time.Time
}

func NotifyAdminSupporterApply(in SupporterApplyInput) {
	fullName := in.FirstName + " " + in.LastName
	card := fmt.Sprintf(`
<p style="margin:0 0 12px;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#9a9a8a;">New supporter application</p>
<h1 style="margin:0 0 20px;font-family:Georgia,'Times New Roman',serif;font-size:24px;font-weight:400;line-height:1.3;color:#1a1a16;">New Supporter Application: %s</h1>
<div style="background:#f4f4f0;border-radius:8px;padding:18px 20px;margin-bottom:28px;">
  <table width="100%%" cellpadding="0" cellspacing="0">
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;width:36%%;">First name</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Last name</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;font-weight:500;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Phone</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">City</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;padding-bottom:8px;">Email</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;padding-bottom:8px;">%s</td>
    </tr>
    <tr>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:12px;color:#888880;text-transform:uppercase;letter-spacing:0.08em;">Applied at</td>
      <td style="font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;color:#1a1a16;">%s</td>
    </tr>
  </table>
</div>`,
		fullName, in.FirstName, in.LastName, in.Phone, in.City, in.Email,
		in.AppliedAt.Format("2006-01-02 15:04 MST"))

	html := wrapEmail("New Supporter Application: "+fullName, card)
	subject := fmt.Sprintf("New Supporter Application: %s", fullName)

	adminEmails := []string{
		"liang.you@horaapp.co",
		"daniele@arcodiax.com",
		"liang.you@arcodiax.com",
	}
	for _, email := range adminEmails {
		if err := SendEmail(EmailPayload{To: email, Subject: subject, Html: html}); err != nil {
			log.Printf("[notify] admin supporter-apply email failed to=%s err=%v", email, err)
		}
	}
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
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
