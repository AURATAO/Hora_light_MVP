package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel/llm"
)

// What a supporter confirms before accepting a task (PLAN 13c, Aura
// 2026-11-16).
//
// The app shows whatever GET /tasks/:id/accept-requirements sends and posts
// back what the supporter accepted; the wording, which task asks for what,
// and how often all live in accept_acks.json. That is the point: an app
// change needs App Review, a change to that file does not.
//
// Not enforced unless ACCEPT_ACK_ENFORCED=1. The web app (app/) shares this
// backend and does not show the sheet yet, so until it does, an accept with
// no acknowledgments still accepts; what a client does send is recorded.

//go:embed accept_acks.json
var acceptAcksJSON []byte

const (
	ackBaseline  = "baseline"
	ackOther     = "other"
	ackHomeEntry = "home_entry"
	ackOnce      = "once"
	ackAlways    = "always"
)

type ackItemConfig struct {
	Version string `json:"version"`
	When    string `json:"when"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

type ackConfig struct {
	Version string                   `json:"version"`
	Heading string                   `json:"heading"`
	Button  string                   `json:"button"`
	Items   map[string]ackItemConfig `json:"items"`
}

// AckItem is one thing to confirm, as the clients read it.
type AckItem struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

var (
	ackConfigOnce sync.Once
	ackConfigVal  *ackConfig
	ackConfigErr  error
)

func parseAckConfig(b []byte) (*ackConfig, error) {
	var cfg ackConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Button) == "" || strings.TrimSpace(cfg.Heading) == "" {
		return nil, fmt.Errorf("accept acks: heading and button are required")
	}
	for _, must := range []string{ackBaseline, ackOther, ackHomeEntry} {
		if _, ok := cfg.Items[must]; !ok {
			return nil, fmt.Errorf("accept acks: item %q is required", must)
		}
	}
	for key, it := range cfg.Items {
		if strings.TrimSpace(it.Version) == "" || strings.TrimSpace(it.Title) == "" || strings.TrimSpace(it.Body) == "" {
			return nil, fmt.Errorf("accept acks: item %q needs a version, a title and a body", key)
		}
		if it.When != ackOnce && it.When != ackAlways {
			return nil, fmt.Errorf("accept acks: item %q: when must be %q or %q", key, ackOnce, ackAlways)
		}
	}
	return &cfg, nil
}

func acceptAcks() (*ackConfig, error) {
	ackConfigOnce.Do(func() { ackConfigVal, ackConfigErr = parseAckConfig(acceptAcksJSON) })
	return ackConfigVal, ackConfigErr
}

func acceptAckEnforced() bool {
	return strings.TrimSpace(os.Getenv("ACCEPT_ACK_ENFORCED")) == "1"
}

func (c *ackConfig) item(key string) AckItem {
	it := c.Items[key]
	return AckItem{Key: key, Version: it.Version, Title: it.Title, Body: it.Body}
}

// ackTextHash: what was on the screen, so a row proves the words and not
// only a version label someone forgot to bump.
func ackTextHash(it AckItem) string {
	sum := sha256.Sum256([]byte(it.Title + "\n" + it.Body))
	return hex.EncodeToString(sum[:])
}

// ackTypeKey: the task's type, read from its own words with the same
// playbook rules the assistant drafted it with. The title decides; the
// description is asked only when the title matches nothing (it carries
// supporter notes that mention other types' words).
func ackTypeKey(cfg *ackConfig, category, title, description string) string {
	if category == "companionship" || category == "companion" {
		if _, ok := cfg.Items["companionship"]; ok {
			return "companionship"
		}
	}
	pbs, err := llm.Playbooks()
	if err != nil || pbs == nil {
		return ackOther
	}
	for _, text := range []string{title, title + "\n" + description} {
		if pb := pbs.Match(text); pb != nil {
			if _, ok := cfg.Items[pb.ID]; ok {
				return pb.ID
			}
		}
	}
	return ackOther
}

func ackEntersHome(title, description string) bool {
	pbs, err := llm.Playbooks()
	if err != nil || pbs == nil {
		return false
	}
	if note := strings.TrimSpace(pbs.ScopeRules.HomeEntry.SupporterNote); note != "" && strings.Contains(description, note) {
		return true
	}
	return pbs.ScopeRules.EntersHome(title + "\n" + description)
}

// acceptRequirementsFor: what this supporter must confirm to accept this
// task, in the order shown. A "once" item already accepted at its current
// version is left out; an "always" item never is.
func acceptRequirementsFor(ctx context.Context, uid, category, title, description string) ([]AckItem, *ackConfig, error) {
	cfg, err := acceptAcks()
	if err != nil {
		return nil, nil, err
	}
	keys := []string{ackBaseline, ackTypeKey(cfg, category, title, description)}
	if ackEntersHome(title, description) {
		keys = append(keys, ackHomeEntry)
	}
	items := make([]AckItem, 0, len(keys))
	for _, key := range keys {
		ic := cfg.Items[key]
		if ic.When == ackOnce {
			var seen bool
			if err := db.QueryRow(ctx, `
				select exists (select 1 from public.supporter_acknowledgments
				                where user_id = $1::uuid and ack_key = $2 and version = $3)`,
				uid, key, ic.Version).Scan(&seen); err != nil {
				return nil, nil, err
			}
			if seen {
				continue
			}
		}
		items = append(items, cfg.item(key))
	}
	return items, cfg, nil
}

type ackSent struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// missingAcks: the required items the client did not send at the version
// required.
func missingAcks(required []AckItem, sent []ackSent) []AckItem {
	have := map[string]bool{}
	for _, s := range sent {
		have[s.Key+"\x00"+s.Version] = true
	}
	var missing []AckItem
	for _, it := range required {
		if !have[it.Key+"\x00"+it.Version] {
			missing = append(missing, it)
		}
	}
	return missing
}

// recordAcks stores the required items the client sent. Anything else in the
// body (an unknown key, an old version) is not what was asked and is not
// recorded.
func recordAcks(ctx context.Context, uid, taskID string, required []AckItem, sent []ackSent) error {
	have := map[string]bool{}
	for _, s := range sent {
		have[s.Key+"\x00"+s.Version] = true
	}
	for _, it := range required {
		if !have[it.Key+"\x00"+it.Version] {
			continue
		}
		if _, err := db.Exec(ctx, `
			insert into public.supporter_acknowledgments (user_id, task_id, ack_key, version, text_sha256)
			values ($1::uuid, $2::uuid, $3, $4, $5)`,
			uid, taskID, it.Key, it.Version, ackTextHash(it)); err != nil {
			return err
		}
	}
	return nil
}

// GET /tasks/:id/accept-requirements
func getAcceptRequirements(c *gin.Context) {
	id := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()
	var requesterID, status, category, title, description string
	if err := db.QueryRow(ctx, `
		select requester_id::text, status, coalesce(category,''), coalesce(title,''), coalesce(description,'')
		  from public.tasks where id = $1::uuid`, id).Scan(&requesterID, &status, &category, &title, &description); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// A task this person could never take answers like one that is not there
	// (the same walls acceptTask tests).
	if allowed, err := sandboxPartitionAllows(ctx, requesterID, meUID); err != nil || !allowed || status == "removed" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if blocked, err := usersBlocked(ctx, meUID, requesterID); err != nil || blocked {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	items, cfg, err := acceptRequirementsFor(ctx, meUID, category, title, description)
	if err != nil {
		// Unreadable requirements must not take the board down: with nothing
		// to show the app accepts as before, and enforcement (when on) still
		// refuses at accept.
		log.Printf("[accept-ack] task=%s requirements unreadable: %v", id, err)
		c.JSON(http.StatusOK, gin.H{"items": []AckItem{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"heading": cfg.Heading, "button": cfg.Button, "items": items})
}

// acceptAckStep runs inside acceptTask before the claim. It records what the
// client acknowledged and, when enforced, refuses an accept that is missing
// a required item (false = the response is written).
func acceptAckStep(c *gin.Context, taskID, meUID string) bool {
	ctx := c.Request.Context()
	var in struct {
		Acknowledged []ackSent `json:"acknowledged"`
	}
	// An accept has never needed a body; an empty or odd one is "nothing sent".
	_ = c.ShouldBindJSON(&in)
	enforced := acceptAckEnforced()
	if len(in.Acknowledged) == 0 && !enforced {
		return true
	}
	var category, title, description string
	err := db.QueryRow(ctx, `
		select coalesce(category,''), coalesce(title,''), coalesce(description,'')
		  from public.tasks where id = $1::uuid`, taskID).Scan(&category, &title, &description)
	var required []AckItem
	var cfg *ackConfig
	if err == nil {
		required, cfg, err = acceptRequirementsFor(ctx, meUID, category, title, description)
	}
	if err == nil {
		if missing := missingAcks(required, in.Acknowledged); enforced && len(missing) > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "acknowledgment_required",
				"message": "Please read and accept the notes for this task first.",
				"heading": cfg.Heading, "button": cfg.Button, "items": missing,
			})
			return false
		}
		err = recordAcks(ctx, meUID, taskID, required, in.Acknowledged)
	}
	if err != nil {
		log.Printf("[accept-ack] task=%s supporter=%s: %v", taskID, meUID, err)
		if enforced {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return false
		}
	}
	return true
}
