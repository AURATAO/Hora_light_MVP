package main

// "I can't do this task" — a supporter telling HO:RA they cannot do a task
// they accepted (Aura, 2026-11-18).
//
// WHY IT EXISTS. The app had no way to say this: the supporter was meant to
// find a contact, type an explanation and wait. The cheaper thing to do is
// nothing, and a supporter who silently stops is the worst case — nobody at
// HO:RA knows, and the requester waits until they complain. The button does
// not prevent a drop; it buys the time between the drop and us knowing.
//
// WHAT IT DOES NOT DO. It changes nothing on the task. The supporter stays
// assigned, a running timer keeps running, the card hold stands, nobody else
// is offered the work. It raises a hand: one audit row, one email to ops.
// What happens next — a hand-over, a cancel, pay for the time logged — is a
// person's decision (skills/supporter-drop-runbook.md). A button that released
// the task would make dropping free, and would do it at exactly the moments
// that are expensive: mid-task, with groceries in a bag.
//
// It therefore touches neither acceptance nor settlement, and needs no
// migration: the record is an audit_logs row (action SUPPORTER_CANT_DO).
//
// The wording — the button, the reasons, the confirmation — is cant_do.json,
// served by GET /config, so it can change without an app release.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

//go:embed cant_do.json
var cantDoJSON []byte

const cantDoAuditAction = "SUPPORTER_CANT_DO"

// A second report inside this window is acknowledged and not sent again: a
// worried supporter taps twice, and ops need one email, not three.
const cantDoRepeatWindow = 10 * time.Minute

const cantDoNoteMax = 500

type cantDoReason struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type cantDoCopy struct {
	Version         string         `json:"version"`
	Button          string         `json:"button"`
	ButtonCaption   string         `json:"button_caption"`
	Heading         string         `json:"heading"`
	Intro           string         `json:"intro"`
	Reasons         []cantDoReason `json:"reasons"`
	BoughtQuestion  string         `json:"bought_question"`
	BoughtYes       string         `json:"bought_yes"`
	BoughtNo        string         `json:"bought_no"`
	NotePlaceholder string         `json:"note_placeholder"`
	Submit          string         `json:"submit"`
	DoneTitle       string         `json:"done_title"`
	DoneBody        string         `json:"done_body"`
	ReportedLabel   string         `json:"reported_label"`
}

var (
	cantDoOnce sync.Once
	cantDoVal  *cantDoCopy
	cantDoErr  error
)

func parseCantDoCopy(b []byte) (*cantDoCopy, error) {
	var c cantDoCopy
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	for name, v := range map[string]string{
		"version": c.Version, "button": c.Button, "heading": c.Heading,
		"bought_question": c.BoughtQuestion, "bought_yes": c.BoughtYes, "bought_no": c.BoughtNo,
		"submit": c.Submit, "done_title": c.DoneTitle, "done_body": c.DoneBody,
		"reported_label": c.ReportedLabel,
	} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("cant_do: %s is required", name)
		}
	}
	if len(c.Reasons) == 0 {
		return nil, errors.New("cant_do: at least one reason is required")
	}
	seen := map[string]bool{}
	for i, r := range c.Reasons {
		if strings.TrimSpace(r.Code) == "" || strings.TrimSpace(r.Label) == "" {
			return nil, fmt.Errorf("cant_do: reason %d needs a code and a label", i)
		}
		if seen[r.Code] {
			return nil, fmt.Errorf("cant_do: reason code %q appears twice", r.Code)
		}
		seen[r.Code] = true
	}
	return &c, nil
}

func cantDoText() (*cantDoCopy, error) {
	cantDoOnce.Do(func() { cantDoVal, cantDoErr = parseCantDoCopy(cantDoJSON) })
	return cantDoVal, cantDoErr
}

func (c *cantDoCopy) reasonLabel(code string) (string, bool) {
	for _, r := range c.Reasons {
		if r.Code == code {
			return r.Label, true
		}
	}
	return "", false
}

// cantDoReportedAt is when this supporter last raised their hand on this task,
// or nil. Read by getTask so the screen can say "we've got it" after a reload.
func cantDoReportedAt(ctx *gin.Context, taskID, uid string) *time.Time {
	var at time.Time
	err := db.QueryRow(ctx.Request.Context(), `
		select max(created_at) from public.audit_logs
		 where job_id = $1::uuid and actor_id = $2::uuid and action = $3
		having max(created_at) is not null
	`, taskID, uid, cantDoAuditAction).Scan(&at)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("[cantDo][reportedAt] task=%s err=%v", taskID, err)
		}
		return nil
	}
	return &at
}

// POST /tasks/:id/cant-do  { "reason": "<code>", "bought_items": true|false, "note": "..." }
func postCantDo(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	taskID := c.Param("id")
	ctx := c.Request.Context()

	copyText, err := cantDoText()
	if err != nil {
		log.Printf("[cantDo][copy] %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unavailable"})
		return
	}

	var in struct {
		Reason      string `json:"reason"`
		BoughtItems *bool  `json:"bought_items"`
		Note        string `json:"note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	reason := strings.TrimSpace(in.Reason)
	reasonLabel, ok := copyText.reasonLabel(reason)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "reason_required",
			"message": "Choose what's going on so we know how to help.",
		})
		return
	}
	// Asked outright rather than defaulted: "has money already been spent"
	// is the one fact that decides what ops do next, and a default of "no"
	// would be wrong exactly when it matters.
	if in.BoughtItems == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bought_items_required",
			"message": "Tell us whether you've bought anything for this task.",
		})
		return
	}
	note := strings.TrimSpace(in.Note)
	if r := []rune(note); len(r) > cantDoNoteMax {
		note = string(r[:cantDoNoteMax])
	}

	var (
		title, status string
		assignedToID  *string
	)
	if err := db.QueryRow(ctx, `
		select coalesce(title,''), status, assigned_to_id::text
		from public.tasks where id = $1::uuid
	`, taskID).Scan(&title, &status, &assignedToID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// Only whoever holds the task, and only while there is something left to
	// do on it. Everyone else gets the same answer a stranger would.
	if assignedToID == nil || *assignedToID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if status != "open" {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "task_closed",
			"message": "This task is already closed, so there's nothing more to do on it.",
		})
		return
	}

	done := gin.H{"ok": true, "title": copyText.DoneTitle, "body": copyText.DoneBody}

	if last := cantDoReportedAt(c, taskID, uid); last != nil && time.Since(*last) < cantDoRepeatWindow {
		done["already_reported"] = true
		done["reported_at"] = last
		c.JSON(http.StatusOK, done)
		return
	}

	var sessions, minutes int
	_ = db.QueryRow(ctx, `
		select count(*),
		       coalesce(sum(ceil(extract(epoch from (coalesce(end_at, now()) - start_at)) / 60.0)), 0)::int
		from public.worklogs where task_id = $1::uuid
	`, taskID).Scan(&sessions, &minutes)

	// The row IS the report. If it cannot be written the supporter must not be
	// told "we've got it" — nobody has.
	if err := writeAuditErr(ctx, taskID, uid, cantDoAuditAction, reason, map[string]any{
		"bought_items":   *in.BoughtItems,
		"note":           note,
		"started":        sessions > 0,
		"minutes_logged": minutes,
		"copy_version":   copyText.Version,
	}); err != nil {
		log.Printf("[cantDo][audit][ERROR] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "not_sent",
			"message": "That didn't reach us. Try again, or message HO:RA directly.",
		})
		return
	}

	emailOpsOfCantDo(c, taskID, title, uid, reasonLabel, *in.BoughtItems, note, sessions > 0, minutes)
	// No PII in logs: ids and the reason code only.
	log.Printf("[cantDo] task=%s supporter=%s reason=%s bought=%t started=%t",
		taskID, uid, reason, *in.BoughtItems, sessions > 0)

	done["reported_at"] = time.Now().UTC()
	c.JSON(http.StatusOK, done)
}

func emailOpsOfCantDo(c *gin.Context, taskID, title, uid, reasonLabel string, bought bool, note string, started bool, minutes int) {
	ctx := c.Request.Context()
	name := html.EscapeString(displayNameForUser(ctx, uid))
	state := "Accepted, not started"
	if started {
		state = fmt.Sprintf("Started — %d min logged", minutes)
	}
	boughtLine := "No"
	if bought {
		boughtLine = "<strong>YES — ask for the receipt photo and where the items are</strong>"
	}
	noteLine := "—"
	if note != "" {
		noteLine = html.EscapeString(note)
	}
	subject := fmt.Sprintf("[HO:RA] Supporter can't do a task: %s", reasonLabel)
	if bought {
		subject += " (items bought)"
	}
	emailOpsAdmins(subject, fmt.Sprintf(`<p><strong>%s</strong> says they can't do <strong>%s</strong>.</p>
<ul>
  <li>Reason: %s</li>
  <li>Bought anything: %s</li>
  <li>Note: %s</li>
  <li>Task state: %s</li>
  <li>Task id: %s</li>
</ul>
<p>Nothing has changed on the task: they are still assigned and the requester has not been told.
Contact the supporter, then Reassign or Cancel in the ops panel &rarr; Task feed.</p>`,
		name, html.EscapeString(title), html.EscapeString(reasonLabel), boughtLine, noteLine,
		html.EscapeString(state), taskID))
}
