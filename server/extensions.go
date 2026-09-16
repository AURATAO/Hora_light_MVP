package main

// Phase 2b: the mid-task ask.
//
// A supporter standing in a shop finds the item costs $8 more than the
// requester budgeted, or the job is plainly going to run twenty minutes past
// the estimate. Both are the same shape of problem — "I need permission for
// something that costs money, and I need it in the next couple of minutes" —
// so both are the same table and the same flow:
//
//	supporter asks  ──> requester gets a push with two buttons
//	                     approve ──> the ceiling moves, the hold grows if needed
//	                     deny    ──> the supporter is told, immediately
//	                     silence ──> 5 minutes later it is a denial
//
// WHY SILENCE IS A DENIAL, AND WHY THE FALLBACK IS CHOSEN UP FRONT. The
// supporter cannot stand in the shop indefinitely, and "the requester never
// answered" has to resolve to an action rather than to a question. So a budget
// request carries the supporter's own pre-selected fallback — buy an
// alternative, or skip the item — which is what they do the moment the answer
// is no OR the five minutes run out. They pick it while they still have the
// context; they are not asked to decide anything under time pressure.
//
// A time request has no fallback field because it needs none: the fallback is
// already the billing. Time past the ceiling is not charged, the supporter
// keeps working or wraps up as they judge safest, and nobody is billed for
// something they did not agree to.
//
// EXPIRY IS LAZY, AND THAT IS THE WHOLE SCHEDULER. There is no timer thread
// and no new cron. A request that is older than the timeout is expired by
// whoever looks at it next — the supporter's polling screen, the requester
// opening the notification, the resolve handler itself, or the 6h pre-auth
// watcher's sweep. The supporter's screen polls every few seconds while a
// request is outstanding, so in practice the expiry lands within seconds of
// the deadline for the one person who is waiting on it, and nothing at all
// runs for the tasks where nobody is.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	notify "hora-auth/internal/notify"
)

// The two kinds, the two terminal-by-silence statuses, and the two fallbacks.
// These strings are enforced by CHECK constraints on extension_requests;
// keeping the Go constants beside the only code that writes them is what stops
// a typo becoming a constraint violation surfaced as "db error".
const (
	extensionKindBudget = "budget"
	extensionKindTime   = "time"

	extensionStatusPending  = "pending"
	extensionStatusApproved = "approved"
	extensionStatusDenied   = "denied"
	extensionStatusExpired  = "expired"

	fallbackBuyAlternative = "buy_alternative"
	fallbackSkipItem       = "skip_item"
)

// The time extensions a requester can grant with one tap. Offered rather than
// typed: the whole value of the flow is that it resolves in one gesture on a
// lock screen, and a free-text minutes field is not that. A supporter who
// needs more than 30 asks twice.
var extensionTimeChoices = []int{15, 30}

// ExtensionRequest is one row of public.extension_requests as both clients see
// it.
type ExtensionRequest struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	SupporterID string `json:"supporter_id"`
	Kind        string `json:"kind"`

	// Exactly one is set, by kind. Both are the ADDITIONAL amount asked for.
	RequestedCents   *int `json:"requested_cents,omitempty"`
	RequestedMinutes *int `json:"requested_minutes,omitempty"`

	// What is STORED: a preset slug, "other: <text>", or free text from a
	// request written before the presets existed.
	Reason string `json:"reason,omitempty"`
	// What is READ: the human sentence for whichever of those it is. Clients
	// render this and never the slug — the requester approving a charge should
	// not be shown "item_unavailable".
	ReasonLabel  string `json:"reason_label,omitempty"`
	Fallback     string `json:"fallback,omitempty"`
	FallbackNote string `json:"fallback_note,omitempty"`

	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	// When silence becomes a denial. Sent so the supporter's screen can count
	// down without knowing BillingConfig — and so it counts down against the
	// server's clock, not the phone's.
	ExpiresAt time.Time `json:"expires_at"`

	// The sentence the supporter's screen shows when this expired: their own
	// fallback, read back to them. Empty for a time request, which has none.
	FallbackInstruction string `json:"fallback_instruction,omitempty"`
}

// Why a supporter needs more money, as a closed set.
//
// This replaced a free-text "Why?" box. Free text on a phone, one-handed, in a
// shop queue, with a five-minute timer running, is a field people leave empty —
// and an empty reason makes the requester's one-tap approval a guess. Four
// options cover every case the beta actually produced, and the fourth is an
// escape hatch rather than a shrug.
//
// STORED AS THE SLUG in extension_requests.reason ("price_higher"), or
// "other: <text>" for the free-form one. The column is unchanged: it was always
// a display string, and rows written before this are free text that
// reasonLabel passes through untouched.
// ORDERED, because it is rendered as a list and a Go map is not. Shipped to
// both clients on GET /tasks/:id/extensions, exactly like time_choices — a
// product vocabulary belongs in one place, and two hardcoded copies of it
// drift the first time a fifth option is added.
type budgetReason struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

var budgetReasons = []budgetReason{
	{"price_higher", "Price higher than listed"},
	{"item_unavailable", "Item unavailable — alternative costs more"},
	{"extra_item", "Requester asked for extra item"},
	// "Other" is not in this list: it is not a stored slug but a mode the form
	// enters, which then stores "other: <what they typed>".
}

var budgetReasonLabels = func() map[string]string {
	m := make(map[string]string, len(budgetReasons))
	for _, r := range budgetReasons {
		m[r.Value] = r.Label
	}
	return m
}()

// otherReasonPrefix marks a reason the supporter typed themselves.
const otherReasonPrefix = "other: "

// maxReasonLength bounds what is stored. The clients cap the Other field at 80
// characters; this is the server saying the same thing to anything that is not
// one of our clients.
const maxReasonLength = 120

// reasonLabel turns what is stored into what a requester reads.
//
// Three cases, and the third is the one that matters: a slug maps to its label,
// an "other: …" reason yields the supporter's own words, and ANYTHING ELSE is
// returned unchanged. That last branch is not a fallback for bad data — it is
// how every request written before the presets existed keeps rendering as the
// sentence its supporter actually typed.
func reasonLabel(reason string) string {
	if reason == "" {
		return ""
	}
	if label, ok := budgetReasonLabels[reason]; ok {
		return label
	}
	if strings.HasPrefix(reason, otherReasonPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(reason, otherReasonPrefix))
	}
	return reason
}

// fallbackInstruction is what the supporter is told to do now.
//
// Their own words where they gave them: "buy an alternative" with no note is
// an instruction to guess, and the note is the entire reason the field exists.
func fallbackInstruction(fallback, note string) string {
	switch fallback {
	case fallbackBuyAlternative:
		if strings.TrimSpace(note) != "" {
			return "Buy the alternative you chose: " + strings.TrimSpace(note)
		}
		return "Buy the alternative you chose."
	case fallbackSkipItem:
		return "Skip this item and carry on with the rest of the task."
	}
	return ""
}

// ── Reading ────────────────────────────────────────────────────────────────

const extensionSelect = `
	select id::text, task_id::text, supporter_id::text, kind,
	       requested_cents, requested_minutes,
	       coalesce(reason,''), coalesce(fallback,''), coalesce(fallback_note,''),
	       status, created_at, resolved_at
	  from public.extension_requests
`

func scanExtension(row interface{ Scan(dest ...any) error }) (ExtensionRequest, error) {
	var e ExtensionRequest
	err := row.Scan(&e.ID, &e.TaskID, &e.SupporterID, &e.Kind,
		&e.RequestedCents, &e.RequestedMinutes,
		&e.Reason, &e.Fallback, &e.FallbackNote,
		&e.Status, &e.CreatedAt, &e.ResolvedAt)
	if err != nil {
		return e, err
	}
	e.ExpiresAt = e.CreatedAt.Add(time.Duration(Billing.ApprovalTimeoutMinutes) * time.Minute)
	e.ReasonLabel = reasonLabel(e.Reason)
	if e.Status == extensionStatusExpired || e.Status == extensionStatusDenied {
		e.FallbackInstruction = fallbackInstruction(e.Fallback, e.FallbackNote)
	}
	return e, nil
}

// expireStaleExtensions is the whole expiry mechanism: one statement, run by
// whoever looked at this task last.
//
// Scoped to one task rather than sweeping the table, because the caller is
// always already looking at exactly one. sweepExpiredExtensions below is the
// unscoped version, for the 6h watcher.
//
// Returns the rows that just expired, so the caller can notify their two
// parties — an expiry nobody is told about is indistinguishable from a request
// that vanished.
func expireStaleExtensions(ctx context.Context, taskID string) []ExtensionRequest {
	rows, err := db.Query(ctx, `
		update public.extension_requests
		   set status = $2, resolved_at = now()
		 where task_id = $1::uuid
		   and status = $3
		   and created_at < now() - make_interval(mins => $4)
		returning id::text, task_id::text, supporter_id::text, kind,
		          requested_cents, requested_minutes,
		          coalesce(reason,''), coalesce(fallback,''), coalesce(fallback_note,''),
		          status, created_at, resolved_at
	`, taskID, extensionStatusExpired, extensionStatusPending, Billing.ApprovalTimeoutMinutes)
	if err != nil {
		log.Printf("[extensions][expire] task=%s: %v", taskID, err)
		return nil
	}
	defer rows.Close()

	var expired []ExtensionRequest
	for rows.Next() {
		e, err := scanExtension(rows)
		if err != nil {
			log.Printf("[extensions][expire] scan task=%s: %v", taskID, err)
			return expired
		}
		expired = append(expired, e)
	}
	return expired
}

// expireAndAnnounce expires what is stale on this task and tells both sides.
//
// The notification is fired on a background context in a goroutine (S-32): the
// caller is usually a GET that some screen is polling, and a mail round-trip
// must not be on that path.
func expireAndAnnounce(ctx context.Context, taskID string) {
	for _, e := range expireStaleExtensions(ctx, taskID) {
		log.Printf("[extensions] expired id=%s task=%s kind=%s after %dm",
			e.ID, e.TaskID, e.Kind, Billing.ApprovalTimeoutMinutes)
		writeAudit(ctx, taskID, systemActorUID, "EXTENSION_EXPIRED", e.Kind, map[string]any{
			"extension_id":      e.ID,
			"requested_cents":   e.RequestedCents,
			"requested_minutes": e.RequestedMinutes,
			"fallback":          e.Fallback,
		})
		req := e
		go announceExtensionResolution(context.Background(), req)
	}
}

// sweepExpiredExtensions is the unscoped backstop, called by the existing 6h
// pre-auth watcher. No new scheduler (the plan's constraint, and the right
// one): a task nobody is looking at has nobody waiting on the answer, so a
// six-hour granularity costs that request nothing — it is already long since
// resolved as far as the supporter's screen is concerned. This exists so the
// row does not sit 'pending' forever in the ledger.
func sweepExpiredExtensions(ctx context.Context) {
	rows, err := db.Query(ctx, `
		update public.extension_requests
		   set status = $1, resolved_at = now()
		 where status = $2
		   and created_at < now() - make_interval(mins => $3)
		returning id::text, task_id::text, kind
	`, extensionStatusExpired, extensionStatusPending, Billing.ApprovalTimeoutMinutes)
	if err != nil {
		log.Printf("[extensions][sweep] %v", err)
		return
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var id, taskID, kind string
		if err := rows.Scan(&id, &taskID, &kind); err != nil {
			log.Printf("[extensions][sweep] scan: %v", err)
			return
		}
		n++
		log.Printf("[extensions][sweep] expired id=%s task=%s kind=%s", id, taskID, kind)
	}
	if n > 0 {
		log.Printf("[extensions][sweep] %d stale request(s) expired", n)
	}
}

// GET /tasks/:id/extensions
//
// Both parties, one payload. The supporter polls it while they wait; the
// requester's task screen reads it to decide whether to draw an approve/deny
// card. Expiry is applied first, which is what makes the supporter's "no
// response — proceed with your fallback" appear within one poll of the
// deadline rather than whenever something else happened to touch the task.
func listTaskExtensions(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !isTaskParty(meUID, t) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	expireAndAnnounce(ctx, taskID)

	rows, err := db.Query(ctx, extensionSelect+` where task_id = $1::uuid order by created_at asc`, taskID)
	if err != nil {
		log.Printf("[extensions][list] task=%s: %v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	items := []ExtensionRequest{}
	for rows.Next() {
		e, err := scanExtension(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		items = append(items, e)
	}

	var approvedBudget int
	_ = db.QueryRow(ctx,
		`select coalesce(shopping_budget_approved_cents, 0) from public.tasks where id=$1::uuid`,
		taskID).Scan(&approvedBudget)
	_, cap := taskTimeCapMinutes(ctx, taskID)

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		// The two numbers the supporter's screen shows at all times, so it
		// never has to reconstruct them from the list above.
		"approved_budget_cents": approvedBudget,
		"time_cap":              cap,
		"time_choices":          extensionTimeChoices,
		// The budget-reason presets, ordered, so the supporter's form renders
		// the same vocabulary the requester's approval card reads back.
		"budget_reasons":  budgetReasons,
		"timeout_minutes": Billing.ApprovalTimeoutMinutes,
		"tolerance_cents": Billing.OverageToleranceCents,
	})
}

// isTaskParty reports whether a uid is the requester or the assigned supporter.
func isTaskParty(uid string, t adminTask) bool {
	if uid == "" {
		return false
	}
	return uid == t.RequesterID || (t.AssigneeID != nil && uid == *t.AssigneeID)
}

// ── Asking ─────────────────────────────────────────────────────────────────

// POST /tasks/:id/budget-increase
//
//	{ "requested_cents": 800, "reason": "…",
//	  "fallback": "buy_alternative", "fallback_note": "the 500g jar instead" }
func requestBudgetIncrease(c *gin.Context) {
	var in struct {
		RequestedCents int    `json:"requested_cents"`
		Reason         string `json:"reason"`
		Fallback       string `json:"fallback"`
		FallbackNote   string `json:"fallback_note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if in.RequestedCents <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_amount",
			"message": "Enter how much more you need.",
		})
		return
	}
	// No upper bound. There is no cap on a shopping budget any more (the $30
	// one refused legitimate tasks), and there is no cap on raising one
	// either: the requester reads the amount on their own screen and taps
	// Approve, which is a better check than a constant. An approved increase
	// raises the ceiling only — nothing is authorized at approval time, and
	// the whole overage is collected at completion.
	if in.Fallback != fallbackBuyAlternative && in.Fallback != fallbackSkipItem {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "fallback_required",
			"message": "Choose what to do if there's no answer: buy an alternative, or skip the item.",
		})
		return
	}

	cents := in.RequestedCents
	createExtension(c, ExtensionRequest{
		Kind:           extensionKindBudget,
		RequestedCents: &cents,
		Reason:         truncateRunes(strings.TrimSpace(in.Reason), maxReasonLength),
		Fallback:       in.Fallback,
		FallbackNote:   strings.TrimSpace(in.FallbackNote),
	})
}

// POST /tasks/:id/time-extension
//
//	{ "requested_minutes": 15 }
func requestTimeExtension(c *gin.Context) {
	var in struct {
		RequestedMinutes int    `json:"requested_minutes"`
		Reason           string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	ok := false
	for _, choice := range extensionTimeChoices {
		if in.RequestedMinutes == choice {
			ok = true
			break
		}
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "invalid_minutes",
			"message":      "Ask for 15 or 30 more minutes.",
			"time_choices": extensionTimeChoices,
		})
		return
	}

	minutes := in.RequestedMinutes
	createExtension(c, ExtensionRequest{
		Kind:             extensionKindTime,
		RequestedMinutes: &minutes,
		Reason:           truncateRunes(strings.TrimSpace(in.Reason), maxReasonLength),
	})
}

// createExtension is the half both asks share: authorize, insert, notify.
func createExtension(c *gin.Context, draft ExtensionRequest) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// The assigned supporter and nobody else. A requester cannot raise their
	// own budget through this door — that is an edit to their own task — and a
	// stranger cannot ask for money on a task they are not working.
	if t.AssigneeID == nil || *t.AssigneeID != meUID {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "not_assigned",
			"message": "Only the supporter working this task can ask for more.",
		})
		return
	}
	// 'open' with an assignee IS accepted/in-progress: the schema has no
	// separate in_progress status, and a task that is completed, cancelled or
	// removed has nothing left to authorize.
	if t.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_active",
			"status":  t.Status,
			"message": fmt.Sprintf("A %s task can't be changed.", t.Status),
		})
		return
	}

	// Clear the deck first: a request that timed out ten minutes ago must not
	// block a fresh one through the partial unique index.
	expireAndAnnounce(ctx, taskID)

	var e ExtensionRequest
	row := db.QueryRow(ctx, `
		insert into public.extension_requests
		  (task_id, supporter_id, kind, requested_cents, requested_minutes,
		   reason, fallback, fallback_note, status)
		values ($1::uuid, $2::uuid, $3, $4, $5, nullif($6,''), nullif($7,''), nullif($8,''), $9)
		returning id::text, task_id::text, supporter_id::text, kind,
		          requested_cents, requested_minutes,
		          coalesce(reason,''), coalesce(fallback,''), coalesce(fallback_note,''),
		          status, created_at, resolved_at
	`, taskID, meUID, draft.Kind, draft.RequestedCents, draft.RequestedMinutes,
		draft.Reason, draft.Fallback, draft.FallbackNote, extensionStatusPending)

	e, err = scanExtension(row)
	if err != nil {
		// The partial unique index fired: there is already a pending request of
		// this kind. 409 rather than 400 — nothing about the request is wrong,
		// it is the state that refuses it, and the client's answer is to show
		// the one already outstanding rather than to correct a field.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "request_already_pending",
				"message": "You already have a request waiting on an answer for this task.",
			})
			return
		}
		log.Printf("[extensions][create] task=%s kind=%s: %v", taskID, draft.Kind, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	writeAudit(ctx, taskID, meUID, "EXTENSION_REQUESTED", e.Kind, map[string]any{
		"extension_id":      e.ID,
		"requested_cents":   e.RequestedCents,
		"requested_minutes": e.RequestedMinutes,
		"fallback":          e.Fallback,
	})
	log.Printf("[extensions] requested id=%s task=%s kind=%s by=%s", e.ID, taskID, e.Kind, meUID)

	announceExtensionRequest(ctx, e, t)

	c.JSON(http.StatusCreated, e)
}

// ── Answering ──────────────────────────────────────────────────────────────

// POST /tasks/:id/extensions/:eid/approve
func approveExtension(c *gin.Context) { resolveExtension(c, extensionStatusApproved) }

// POST /tasks/:id/extensions/:eid/deny
func denyExtension(c *gin.Context) { resolveExtension(c, extensionStatusDenied) }

func resolveExtension(c *gin.Context, decision string) {
	taskID := c.Param("id")
	extID := c.Param("eid")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// The requester alone. They are the one being asked to pay.
	if t.RequesterID != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Expire first, so a tap that lands a second after the deadline is honestly
	// refused as expired rather than quietly charging the requester for
	// something the supporter has already given up on and worked around.
	expireAndAnnounce(ctx, taskID)

	// The status re-test inside the UPDATE is what makes a double-tap and two
	// racing devices safe: the second one matches no row.
	row := db.QueryRow(ctx, `
		update public.extension_requests
		   set status = $3, resolved_at = now(), resolved_by = $4::uuid
		 where id = $1::uuid and task_id = $2::uuid and status = $5
		returning id::text, task_id::text, supporter_id::text, kind,
		          requested_cents, requested_minutes,
		          coalesce(reason,''), coalesce(fallback,''), coalesce(fallback_note,''),
		          status, created_at, resolved_at
	`, extID, taskID, decision, meUID, extensionStatusPending)

	e, err := scanExtension(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already answered, already expired, or not on this task. Read the row
		// back so the answer says which — "it expired 20 seconds ago" and "you
		// already approved this" are different facts to the person tapping.
		current, readErr := scanExtension(db.QueryRow(ctx,
			extensionSelect+` where id = $1::uuid and task_id = $2::uuid`, extID, taskID))
		if readErr != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{
			"error":   "request_already_resolved",
			"status":  current.Status,
			"message": extensionResolvedMessage(current.Status),
			"request": current,
		})
		return
	}
	if err != nil {
		log.Printf("[extensions][resolve] id=%s task=%s: %v", extID, taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// The ceiling moves only on an approval, and ONLY the ceiling. Nothing is
	// authorized here: an approval is permission to spend, not a charge, and
	// everything an approval makes possible is collected at completion against
	// the hold and — if it outgrew the hold — a balance charge.
	//
	// This used to attempt a Stripe incremental authorization, then fall back
	// to opening a second hold, all inside the requester's Approve tap. It is
	// gone: it put two network round trips in a one-tap interaction, it could
	// half-succeed, and it existed only to keep the capture inside a hold the
	// system no longer over-sizes.
	if decision == extensionStatusApproved {
		applyApprovedExtension(ctx, taskID, e)
	}

	meta := map[string]any{
		"extension_id":      e.ID,
		"kind":              e.Kind,
		"requested_cents":   e.RequestedCents,
		"requested_minutes": e.RequestedMinutes,
	}
	writeAudit(ctx, taskID, meUID, "EXTENSION_"+strings.ToUpper(decision), e.Kind, meta)
	log.Printf("[extensions] %s id=%s task=%s kind=%s by=%s", decision, e.ID, taskID, e.Kind, meUID)

	go announceExtensionResolution(context.Background(), e)

	approvedBudget, capDetail := postResolutionState(ctx, taskID)
	c.JSON(http.StatusOK, gin.H{
		"request":               e,
		"approved_budget_cents": approvedBudget,
		"time_cap":              capDetail,
	})
}

func postResolutionState(ctx context.Context, taskID string) (int, TimeCap) {
	var approvedBudget int
	_ = db.QueryRow(ctx,
		`select coalesce(shopping_budget_approved_cents, 0) from public.tasks where id=$1::uuid`,
		taskID).Scan(&approvedBudget)
	_, capDetail := taskTimeCapMinutes(ctx, taskID)
	return approvedBudget, capDetail
}

func extensionResolvedMessage(status string) string {
	switch status {
	case extensionStatusApproved:
		return "This request was already approved."
	case extensionStatusDenied:
		return "This request was already declined."
	case extensionStatusExpired:
		return "This request timed out before it was answered. Your supporter has moved on with their fallback."
	}
	return "This request has already been answered."
}

// applyApprovedExtension moves the ceiling the approval raised. That is all it
// does — there is no money side to an approval any more.
func applyApprovedExtension(ctx context.Context, taskID string, e ExtensionRequest) {
	switch e.Kind {
	case extensionKindBudget:
		if e.RequestedCents == nil {
			return
		}
		if _, err := db.Exec(ctx, `
			update public.tasks
			   set shopping_budget_approved_cents = coalesce(shopping_budget_approved_cents, 0) + $2
			 where id = $1::uuid
		`, taskID, *e.RequestedCents); err != nil {
			log.Printf("[extensions][ERROR] approved budget increase id=%s not applied to task=%s: %v",
				e.ID, taskID, err)
			return
		}

	case extensionKindTime:
		// Nothing to write: taskTimeCapMinutes sums approved time requests, so
		// the row this handler just wrote IS the new ceiling. What does have to
		// happen is resetting the three latches, so the supporter gets a fresh
		// warning as the NEW ceiling approaches instead of silently running
		// into it having already been warned about the old one.
		if _, err := db.Exec(ctx, `
			update public.tasks
			   set time_cap_warned_at = null,
			       time_cap_reached_at = null,
			       time_cap_ops_alerted_at = null
			 where id = $1::uuid
		`, taskID); err != nil {
			log.Printf("[extensions][ERROR] could not reset time-cap latches task=%s: %v", taskID, err)
		}
	}
}

// ── Telling people ─────────────────────────────────────────────────────────

func announceExtensionRequest(ctx context.Context, e ExtensionRequest, t adminTask) {
	var ntype, title, body string
	switch e.Kind {
	case extensionKindBudget:
		ntype = "BUDGET_INCREASE_REQUESTED"
		title = "Your supporter needs a bigger budget"
		body = fmt.Sprintf("%s is asking for %s more for %q.",
			displayName(t.AssigneeEmail), formatCentsUSD(derefInt(e.RequestedCents)), t.Title)
		if label := reasonLabel(e.Reason); label != "" {
			body += " " + label
		}
		body += fmt.Sprintf(" Approve or decline in the app — after %d minutes it's automatically declined.",
			Billing.ApprovalTimeoutMinutes)
	default:
		ntype = "TIME_EXTENSION_REQUESTED"
		title = "Your supporter needs more time"
		body = fmt.Sprintf("%s is asking for %d more minutes on %q.",
			displayName(t.AssigneeEmail), derefInt(e.RequestedMinutes), t.Title)
		if label := reasonLabel(e.Reason); label != "" {
			body += " " + label
		}
		body += fmt.Sprintf(" Approve or decline in the app — after %d minutes it's automatically declined.",
			Billing.ApprovalTimeoutMinutes)
	}

	// Not a goroutine: this is the point of the request, and a requester who
	// never gets the push has been asked nothing. notify.Create already pushes
	// asynchronously; only the email is on this path.
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:        e.TaskID,
		Type:          ntype,
		Title:         title,
		Body:          body,
		TaskTitle:     t.Title,
		SupporterName: displayName(t.AssigneeEmail),
	})
}

// announceExtensionResolution tells both parties how it ended — including on an
// expiry, where nobody decided anything and the requester needs to know that
// their silence WAS the answer.
func announceExtensionResolution(ctx context.Context, e ExtensionRequest) {
	t, err := loadAdminTask(ctx, e.TaskID)
	if err != nil {
		log.Printf("[extensions][notify] task=%s gone: %v", e.TaskID, err)
		return
	}

	ask := fmt.Sprintf("%d more minutes", derefInt(e.RequestedMinutes))
	if e.Kind == extensionKindBudget {
		ask = formatCentsUSD(derefInt(e.RequestedCents)) + " more budget"
	}

	var supporterTitle, supporterBody, requesterTitle, requesterBody string
	switch e.Status {
	case extensionStatusApproved:
		supporterTitle = "Approved — go ahead"
		supporterBody = fmt.Sprintf("%s approved %s on %q.", displayName(t.RequesterEmail), ask, t.Title)
		requesterTitle = "You approved the request"
		requesterBody = fmt.Sprintf("You approved %s on %q.", ask, t.Title)
	case extensionStatusDenied:
		supporterTitle = "Not approved"
		supporterBody = fmt.Sprintf("%s declined %s on %q.", displayName(t.RequesterEmail), ask, t.Title)
		if inst := fallbackInstruction(e.Fallback, e.FallbackNote); inst != "" {
			supporterBody += " " + inst
		}
		requesterTitle = "You declined the request"
		requesterBody = fmt.Sprintf("You declined %s on %q.", ask, t.Title)
	default: // expired
		supporterTitle = "No response"
		supporterBody = fmt.Sprintf("There was no answer within %d minutes on %q.",
			Billing.ApprovalTimeoutMinutes, t.Title)
		if inst := fallbackInstruction(e.Fallback, e.FallbackNote); inst != "" {
			supporterBody += " " + inst
		}
		requesterTitle = "A request timed out"
		requesterBody = fmt.Sprintf("Your supporter asked for %s on %q and didn't hear back within %d minutes, so it was declined automatically.",
			ask, t.Title, Billing.ApprovalTimeoutMinutes)
	}

	notifyUser(ctx, e.SupporterID, t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID:    e.TaskID,
		Type:      "EXTENSION_RESOLVED",
		Title:     supporterTitle,
		Body:      supporterBody,
		TaskTitle: t.Title,
	})
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:    e.TaskID,
		Type:      "EXTENSION_RESOLVED",
		Title:     requesterTitle,
		Body:      requesterBody,
		TaskTitle: t.Title,
	})
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// truncateRunes bounds a stored string by RUNES, not bytes.
//
// Distinct from talkjs_admin.go's byte-slicing truncate, which is fine for the
// log lines it clips but would cut a multi-byte character in half here — that
// stores invalid UTF-8 and renders as a replacement glyph in the requester's
// approval card, on a screen where they are deciding whether to spend money.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
