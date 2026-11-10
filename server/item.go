package main

// ITEM PROBLEM ON A SHOPPING TASK (Aura 2026-11-07).
//
// A supporter in a shop finds an item out of stock. Three cases, chosen by
// the supporter in the app:
//
//  1. Out of stock, and an alternative that fits the budget. The requester
//     is asked one question — "<store> is out of <item>. Your supporter found
//     <alternative> ($<price>). Reply 1 for <alternative>, 2 to skip it." —
//     through the same extension_requests row and the same one-at-a-time
//     approval queue as a budget ask (kind='item'). approved = buy the
//     alternative, denied = skip it. No answer within the approval timeout:
//     the supporter decides, shown the requester's substitution preference.
//  2. Out of stock, nothing similar. A notice, never a question: "…there's
//     nothing similar. Your supporter will skip it." No row.
//  3. The alternative costs more than the budget. That is the existing
//     request-more-money flow (POST /tasks/:id/budget-increase), unchanged —
//     and the reason this endpoint refuses a price above the budget: an item
//     question never moves money, so "1" can never mean spending more.
//
// THE BUDGET CHECK. Nothing in the system tracks what has been spent so far
// mid-task, only the approved ceiling (tasks.shopping_budget_approved_cents).
// So the server refuses an alternative priced above the ceiling, and the
// supporter's form sends "costs more than the budget" to the budget ask. The
// binding check is still the receipt at completion (receiptWithinTolerance):
// no YES, no spending past the ceiling, whatever was said by text.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"

	notify "hora-auth/internal/notify"
	tracing "hora-auth/internal/tracing"
)

const (
	maxItemNameLength = 80
	maxStoreLength    = 60
)

// POST /tasks/:id/item-problem
//
//	{ "item_name": "oat milk", "store_name": "Trader Joe's",
//	  "outcome": "alternative" | "none",
//	  "alternative_name": "almond milk", "alternative_cents": 449,
//	  "alternative_photo_url": "<the URL /completion-photo returned>" }
func reportItemProblem(c *gin.Context) {
	var in struct {
		ItemName            string `json:"item_name"`
		StoreName           string `json:"store_name"`
		Outcome             string `json:"outcome"`
		AlternativeName     string `json:"alternative_name"`
		AlternativeCents    *int   `json:"alternative_cents"`
		AlternativePhotoURL string `json:"alternative_photo_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	taskID := c.Param("id")
	ctx, span := tracing.StartRootSpan(c.Request.Context(), "task.item_problem", tracing.KindChain, "app", c.GetString("uid"), taskID)
	defer span.End()
	span.Metadata(map[string]any{"hora.task_id": taskID, "hora.item_outcome": in.Outcome})

	item := cleanItemText(in.ItemName, maxItemNameLength)
	store := cleanItemText(in.StoreName, maxStoreLength)
	if item == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "item_required", "message": "Say which item is out of stock."})
		return
	}

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	meUID := c.GetString("uid")
	if t.AssigneeID == nil || *t.AssigneeID != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_assigned", "message": "Only the supporter working this task can report an item."})
		return
	}
	if t.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_not_active", "status": t.Status, "message": fmt.Sprintf("A %s task can't be changed.", t.Status)})
		return
	}
	ceiling, err := approvedBudgetCents(ctx, taskID)
	if err != nil {
		log.Printf("[item] task=%s: read budget: %v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if ceiling <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_a_shopping_task", "message": "This task has no shopping budget."})
		return
	}

	switch in.Outcome {
	case "none":
		// A notice, not a question: nothing to answer, so no row and no
		// queue. The audit row is the record.
		writeAudit(ctx, taskID, meUID, "ITEM_UNAVAILABLE", "item", map[string]any{"item": item, "store": store})
		announceItemUnavailable(ctx, taskID, t, item, store)
		span.Output("notice")
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	case "alternative":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "outcome_required", "message": "Choose: an alternative, or nothing similar."})
		return
	}

	alt := cleanItemText(in.AlternativeName, maxItemNameLength)
	if alt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alternative_required", "message": "Name the alternative you found."})
		return
	}
	if in.AlternativeCents == nil || *in.AlternativeCents < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "price_required", "message": "Enter the alternative's price."})
		return
	}
	if *in.AlternativeCents > ceiling {
		// Case 3: more money. The client routes this to the budget ask; a
		// direct call is told the same.
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":                 "over_budget",
			"message":               "That's more than the budget. Ask for more budget instead.",
			"approved_budget_cents": ceiling,
		})
		return
	}
	photo := ""
	if raw := strings.TrimSpace(in.AlternativePhotoURL); raw != "" {
		key, ok := taskPhotoObjectKey(raw)
		if !ok || !strings.HasPrefix(key, "completions/"+taskID+"/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_photo", "message": "Upload the photo again."})
			return
		}
		photo = canonicalTaskPhotoURL(raw)
	}
	span.Metadata(map[string]any{"hora.alternative_cents": *in.AlternativeCents, "hora.has_photo": photo != ""})

	cents := *in.AlternativeCents
	createExtension(c, ExtensionRequest{
		Kind:                extensionKindItem,
		ItemName:            item,
		StoreName:           store,
		AlternativeName:     alt,
		AlternativeCents:    &cents,
		AlternativePhotoURL: photo,
	})
	span.Output(fmt.Sprintf("status %d", c.Writer.Status()))
}

// cleanItemText is a supporter's free text made fit for sentences HO:RA sends
// in its own voice (push, email, text): one line, printable characters only,
// no quotation marks or angle brackets, capped. Cleaned once, at intake, so
// every later message reads the same safe value.
func cleanItemText(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.Join(strings.Fields(s), " ") {
		if unicode.IsPrint(r) && !strings.ContainsRune("\"\u201c\u201d<>", r) {
			b.WriteRune(r)
		}
	}
	return truncateRunes(strings.TrimSpace(b.String()), max)
}

func approvedBudgetCents(ctx context.Context, taskID string) (int, error) {
	var cents int
	err := db.QueryRow(ctx,
		`select coalesce(shopping_budget_approved_cents, 0) from public.tasks where id = $1::uuid`, taskID).Scan(&cents)
	return cents, err
}

// itemProblemLabel is the request in one line, for both clients and the
// record: "Out of oat milk at Trader Joe's: almond milk ($4.49) instead?"
func itemProblemLabel(e ExtensionRequest) string {
	where := ""
	if e.StoreName != "" {
		where = " at " + e.StoreName
	}
	return fmt.Sprintf("Out of %s%s. Alternative: %s (%s).", e.ItemName, where, e.AlternativeName, formatCentsUSD(derefInt(e.AlternativeCents)))
}

// itemInstruction is what the supporter does now, by outcome.
func itemInstruction(e ExtensionRequest) string {
	switch e.Status {
	case extensionStatusApproved:
		return fmt.Sprintf("Buy %s instead of %s.", e.AlternativeName, e.ItemName)
	case extensionStatusDenied:
		return fmt.Sprintf("Skip %s.", e.ItemName)
	case extensionStatusExpired:
		return fmt.Sprintf("No answer. It's your call: buy %s if it suits what they asked for, or skip %s.", e.AlternativeName, e.ItemName)
	}
	return ""
}

// substitutionPreference reads the requester's "Substitution preference: …"
// out of the task description (written there by the text channel's groceries
// playbook as a requester note). "" when there is none.
func substitutionPreference(description string) string {
	const marker = "Substitution preference:"
	i := strings.Index(description, marker)
	if i < 0 {
		return ""
	}
	rest := description[i+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return truncateRunes(strings.TrimSpace(rest), 200)
}

// itemExtras fills what listTaskExtensions adds to an item row for the task's
// two parties: the signed photo and the requester's preference.
func itemExtras(ctx context.Context, taskID string, items []ExtensionRequest) {
	needPref := false
	for i := range items {
		if items[i].Kind != extensionKindItem {
			continue
		}
		needPref = true
		if items[i].AlternativePhotoURL != "" {
			if signed, reason := signedTaskPhotoURL(items[i].AlternativePhotoURL, taskID, taskPhotoLinkTTL); signed != "" {
				items[i].AlternativePhotoSignedURL = signed
			} else {
				log.Printf("[item] task=%s ext=%s: photo not signed: %s", taskID, items[i].ID, reason)
			}
		}
	}
	if !needPref {
		return
	}
	var desc string
	if err := db.QueryRow(ctx, `select coalesce(description,'') from public.tasks where id = $1::uuid`, taskID).Scan(&desc); err != nil {
		log.Printf("[item] task=%s: read description: %v", taskID, err)
		return
	}
	pref := substitutionPreference(desc)
	for i := range items {
		if items[i].Kind == extensionKindItem {
			items[i].SubstitutionPreference = pref
		}
	}
}

// announceItemUnavailable tells the requester an item is skipped. Type
// ITEM_UNAVAILABLE reaches the text channel as a plain status line
// (channel.StatusText), never as a question.
func announceItemUnavailable(ctx context.Context, taskID string, t adminTask, item, store string) {
	body := itemUnavailableBody(item, store)
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "ITEM_UNAVAILABLE",
		Title:         "An item is out of stock",
		Body:          body,
		TaskTitle:     t.Title,
		SupporterName: t.AssigneeName,
	})
}

func itemUnavailableBody(item, store string) string {
	who := "Your supporter"
	if store != "" {
		return fmt.Sprintf("%s is out of %s, and there's nothing similar. %s will skip it.", store, item, who)
	}
	return fmt.Sprintf("%s is out of stock, and there's nothing similar. %s will skip it.", capitalizeFirst(item), who)
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// itemOutOfLine is "<store> is out of <item>." / "<Item> is out of stock."
func itemOutOfLine(e ExtensionRequest) string {
	if e.StoreName != "" {
		return fmt.Sprintf("%s is out of %s.", e.StoreName, e.ItemName)
	}
	return fmt.Sprintf("%s is out of stock.", capitalizeFirst(e.ItemName))
}

// announceItemResolution tells both parties how an item question ended. The
// requester's copy carries ExtensionID, which is what clears the armed
// question in the text channel when it was answered in the app or expired.
func announceItemResolution(ctx context.Context, e ExtensionRequest, t adminTask) {
	var supporterTitle, supporterBody, requesterTitle, requesterBody string
	switch e.Status {
	case extensionStatusApproved:
		supporterTitle = "Get the alternative"
		supporterBody = fmt.Sprintf("%s chose %s instead of %s on %q.", t.RequesterName, e.AlternativeName, e.ItemName, t.Title)
		requesterTitle = "Alternative chosen"
		requesterBody = fmt.Sprintf("You chose %s instead of %s on %q.", e.AlternativeName, e.ItemName, t.Title)
	case extensionStatusDenied:
		supporterTitle = "Skip the item"
		supporterBody = fmt.Sprintf("%s would rather skip %s on %q.", t.RequesterName, e.ItemName, t.Title)
		requesterTitle = "Item skipped"
		requesterBody = fmt.Sprintf("You chose to skip %s on %q.", e.ItemName, t.Title)
	default: // expired
		supporterTitle = "No answer: your call"
		supporterBody = fmt.Sprintf("There was no answer within %d minutes on %q. %s", Billing.ApprovalTimeoutMinutes, t.Title, itemInstruction(e))
		requesterTitle = "A request timed out" // the text channel keys its timeout line on this title (status.go)
		requesterBody = fmt.Sprintf("Your supporter asked about %s on %q and didn't hear back within %d minutes, so they'll decide using what you asked for.",
			e.ItemName, t.Title, Billing.ApprovalTimeoutMinutes)
	}
	notifyUser(ctx, e.SupporterID, t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID: e.TaskID, Type: "EXTENSION_RESOLVED", Title: supporterTitle, Body: supporterBody, TaskTitle: t.Title,
	})
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID: e.TaskID, ExtensionID: e.ID, Type: "EXTENSION_RESOLVED", Title: requesterTitle, Body: requesterBody, TaskTitle: t.Title,
	})
}
