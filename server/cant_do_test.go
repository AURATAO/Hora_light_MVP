package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func callCantDo(t *testing.T, taskID, uid, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/cant-do", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	postCantDo(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// What the task looks like to everything that reads it: who holds it, whether
// it is open, and how many sessions are on it. The button must leave all of
// that exactly as it found it.
func taskFingerprint(t *testing.T, taskID string) string {
	t.Helper()
	return scalar[string](t, `
		select status || '|' || coalesce(assigned_to_id::text,'') || '|' || coalesce(assigned_to,'') || '|' ||
		       (select count(*) from public.worklogs w where w.task_id = t.id)::text || '|' ||
		       (select count(*) from public.worklogs w where w.task_id = t.id and w.end_at is null)::text
		from public.tasks t where t.id = $1::uuid`, taskID)
}

func TestCantDoRecordsAndChangesNothingOnTheTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 12, false) // a session still running
	before := taskFingerprint(t, w.taskID)

	code, out := callCantDo(t, w.taskID, w.supporterID,
		`{"reason":"unsafe","bought_items":true,"note":"  dog loose in the hallway  "}`)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("want 200 ok, got %d (%v)", code, out)
	}
	if out["title"] != "We've got it" || out["body"] == "" {
		t.Errorf("confirmation = %v / %v", out["title"], out["body"])
	}
	// The whole point: a raised hand, not a released task.
	if after := taskFingerprint(t, w.taskID); after != before {
		t.Errorf("the task changed: %q → %q", before, after)
	}

	rows := auditRows(t, w.taskID, cantDoAuditAction)
	if len(rows) != 1 || rows[0].ActorID != w.supporterID || rows[0].Reason != "unsafe" {
		t.Fatalf("audit rows = %+v", rows)
	}
	var meta map[string]any
	_ = json.Unmarshal([]byte(rows[0].Meta), &meta)
	if meta["bought_items"] != true || meta["note"] != "dog loose in the hallway" || meta["started"] != true {
		t.Errorf("audit meta = %v", meta)
	}
	if min, _ := meta["minutes_logged"].(float64); min < 12 {
		t.Errorf("minutes_logged = %v, want >= 12", meta["minutes_logged"])
	}
	// Nothing is sent to the requester: ops decide what they are told.
	if n := scalar[int](t, `select count(*) from public.notifications where task_id=$1::uuid`, w.taskID); n != 0 {
		t.Errorf("%d notifications were written — the requester must not hear about it from the button", n)
	}
}

// A second tap inside the window is acknowledged, not recorded or emailed again.
func TestCantDoRepeatIsAcknowledgedOnce(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	body := `{"reason":"timing","bought_items":false}`
	if code, out := callCantDo(t, w.taskID, w.supporterID, body); code != http.StatusOK {
		t.Fatalf("first: %d (%v)", code, out)
	}
	code, out := callCantDo(t, w.taskID, w.supporterID, body)
	if code != http.StatusOK || out["already_reported"] != true {
		t.Fatalf("second: want 200 already_reported, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, cantDoAuditAction)); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
}

func TestCantDoIsOnlyForWhoeverHoldsTheTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	body := `{"reason":"other","bought_items":false}`
	for name, uid := range map[string]string{"the requester": w.requesterID, "an admin": w.adminID} {
		if code, out := callCantDo(t, w.taskID, uid, body); code != http.StatusForbidden {
			t.Errorf("%s got %d (%v), want 403", name, code, out)
		}
	}
	if n := len(auditRows(t, w.taskID, cantDoAuditAction)); n != 0 {
		t.Errorf("a refused report was recorded (%d rows)", n)
	}
}

func TestCantDoNeedsAReasonAndTheBoughtAnswer(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	cases := []struct{ body, wantErr string }{
		{`{"bought_items":false}`, "reason_required"},
		{`{"reason":"made_up","bought_items":false}`, "reason_required"},
		// "No" is never assumed: it decides what ops do next.
		{`{"reason":"unsafe"}`, "bought_items_required"},
	}
	for _, c := range cases {
		code, out := callCantDo(t, w.taskID, w.supporterID, c.body)
		if code != http.StatusBadRequest || out["error"] != c.wantErr {
			t.Errorf("%s → %d %v, want 400 %s", c.body, code, out["error"], c.wantErr)
		}
	}
	if n := len(auditRows(t, w.taskID, cantDoAuditAction)); n != 0 {
		t.Errorf("an invalid report was recorded (%d rows)", n)
	}
}

func TestCantDoOnAClosedTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "completed")
	code, out := callCantDo(t, w.taskID, w.supporterID, `{"reason":"other","bought_items":false}`)
	if code != http.StatusConflict || out["error"] != "task_closed" {
		t.Fatalf("want 409 task_closed, got %d (%v)", code, out)
	}
}

func TestCantDoNoteIsCapped(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	long := strings.Repeat("é", cantDoNoteMax+200)
	if code, out := callCantDo(t, w.taskID, w.supporterID,
		`{"reason":"other","bought_items":false,"note":"`+long+`"}`); code != http.StatusOK {
		t.Fatalf("%d (%v)", code, out)
	}
	got := scalar[int](t, `
		select char_length(meta->>'note') from public.audit_logs
		where job_id=$1::uuid and action=$2`, w.taskID, cantDoAuditAction)
	if got != cantDoNoteMax {
		t.Errorf("stored note is %d characters, want %d", got, cantDoNoteMax)
	}
	_ = context.Background()
}

// The shipped copy parses, every reason has a distinct code, and GET /config
// carries it — the app shows no button without it.
func TestCantDoCopyIsServedByConfig(t *testing.T) {
	cd, err := cantDoText()
	if err != nil {
		t.Fatalf("cant_do.json: %v", err)
	}
	if cd.Button == "" || len(cd.Reasons) < 2 {
		t.Fatalf("copy = %+v", cd)
	}
	if _, ok := cd.reasonLabel("unsafe"); !ok {
		t.Error(`the "unsafe" reason is gone — the runbook treats it as always valid`)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/config", nil)
	getAppConfig(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	got, _ := out["cant_do"].(map[string]any)
	if got == nil || got["button"] != cd.Button {
		t.Fatalf("/config cant_do = %v", out["cant_do"])
	}
	if _, leaked := got["_comment"]; leaked {
		t.Error("the JSON file's _comment reached the app")
	}
}

func TestParseCantDoCopyRejectsBrokenFiles(t *testing.T) {
	for name, raw := range map[string]string{
		"no reasons":      `{"version":"x","button":"b","heading":"h","bought_question":"q","bought_yes":"y","bought_no":"n","submit":"s","done_title":"t","done_body":"d","reported_label":"r","reasons":[]}`,
		"duplicate codes": `{"version":"x","button":"b","heading":"h","bought_question":"q","bought_yes":"y","bought_no":"n","submit":"s","done_title":"t","done_body":"d","reported_label":"r","reasons":[{"code":"a","label":"A"},{"code":"a","label":"B"}]}`,
		"no button":       `{"version":"x","heading":"h","bought_question":"q","bought_yes":"y","bought_no":"n","submit":"s","done_title":"t","done_body":"d","reported_label":"r","reasons":[{"code":"a","label":"A"}]}`,
	} {
		if _, err := parseCantDoCopy([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
