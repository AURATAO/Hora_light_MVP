//go:build e2ereassign

package main

// End-to-end run of the two drop-off messages (supporter dropped → cancel;
// reassign before/after start) and the customer's replies to them, against a
// THROWAWAY local Postgres with every migration applied, a fake Sendblue and
// TalkJS over HTTP, and the real chat models. Real handlers throughout: the
// Sendblue webhook route, POST /admin/tasks/:id/cancel and
// POST /admin/tasks/:id/reassign (ops auth replaced by a fixed admin
// identity), notify.Create → the chat status hook → Sendblue.
//
//	E2E_DATABASE_URL=postgres://postgres:test@localhost:55447/hora \
//	ANTHROPIC_API_KEY=… go test -tags e2ereassign -run TestE2E -v -timeout 60m .
//
// The local DB is every migration in supabase/migrations applied in order,
// with the pg_net / http / pg_stat_statements / supabase_vault extensions and
// the two triggers that call the production edge function (notify-new-task)
// left out. E2E_ONLY=<substring> runs some cases.
//
// Never point E2E_DATABASE_URL at Supabase: it inserts users and tasks.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"hora-auth/internal/notify"
)

type e2eOut struct {
	To, Text string
	At       time.Time
}

type e2eFake struct {
	mu   sync.Mutex
	out  []e2eOut
	n    int
	talk []string
}

func (f *e2eFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(r.URL.Path, "/api/send-message") {
		var m struct {
			Number  string `json:"number"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(body, &m)
		f.mu.Lock()
		f.n++
		f.out = append(f.out, e2eOut{To: m.Number, Text: m.Content, At: time.Now()})
		id := fmt.Sprintf("out-%d", f.n)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message_handle":%q,"status":"QUEUED"}`, id)
		return
	}
	// TalkJS: accept everything; remember what was posted.
	f.mu.Lock()
	f.talk = append(f.talk, r.Method+" "+r.URL.Path+" "+string(body))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `{"data":[]}`)
		return
	}
	fmt.Fprint(w, `{}`)
}

func (f *e2eFake) since(i int) []e2eOut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]e2eOut(nil), f.out[i:]...)
}

func (f *e2eFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.out)
}

type e2e struct {
	t     *testing.T
	fake  *e2eFake
	srv   *httptest.Server
	sql   *sql.DB
	seq   int
	admin string
}

const e2eOpsNumber = "+12125550999"

func setupE2E(t *testing.T) *e2e {
	dsn := os.Getenv("E2E_DATABASE_URL")
	if dsn == "" || strings.Contains(dsn, "supabase") {
		t.Skip("E2E_DATABASE_URL (local, throwaway) not set")
	}
	fake := &e2eFake{}
	fsrv := httptest.NewServer(fake)
	t.Cleanup(fsrv.Close)
	for k, v := range map[string]string{
		"CHANNEL_WEBHOOKS_ENABLED": "1",
		"SENDBLUE_API_KEY":         "k", "SENDBLUE_API_SECRET": "s",
		"SENDBLUE_FROM_NUMBER":    "+12125550000",
		"SENDBLUE_WEBHOOK_SECRET": "whsec",
		"SENDBLUE_BASE_URL":       fsrv.URL,
		"TALKJS_APP_ID":           "app", "TALKJS_SECRET_KEY": "sk",
		"CHANNEL_AGENT":          "1",
		"CHANNEL_SETUP_BASE_URL": "https://example.test",
		"CHANNEL_OPS_NUMBERS":    e2eOpsNumber,
		"COMPANIONSHIP_ENABLED":  os.Getenv("E2E_COMPANIONSHIP"),
		"POSTMARK_API_TOKEN":     "",
		"STRIPE_SECRET_KEY":      "",
		"GOOGLE_MAPS_API_KEY":    "",
	} {
		t.Setenv(k, v)
	}
	talkjsAPIBase = fsrv.URL

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	db = pool
	sqldb = stdlib.OpenDB(*pool.Config().ConnConfig)

	r := gin.New()
	RegisterChannelWebhooks(r, sqldb)
	adminEmail := "ops-e2e@hora.test"
	opsAdmins = map[string]struct{}{adminEmail: {}}
	var adminID string
	if err := sqldb.QueryRow(`insert into users(email,name) values ($1,'Ops') returning id::text`, adminEmail).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	asAdmin := func(c *gin.Context) { c.Set("uid", adminID); c.Set("email", adminEmail); c.Next() }
	r.POST("/admin/tasks/:id/reassign", asAdmin, requireOpsAdmin(), adminReassignTask)
	r.POST("/admin/tasks/:id/cancel", asAdmin, requireOpsAdmin(), adminCancelTaskHandler)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { notify.ChatHook = nil })
	return &e2e{t: t, fake: fake, srv: srv, sql: sqldb, admin: adminID}
}

func (e *e2e) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.sql.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// customer: a linked requester with an iMessage number.
func (e *e2e) customer(phone, name string) string {
	var uid string
	email := strings.TrimPrefix(phone, "+") + "@cust.test"
	if err := e.sql.QueryRow(`insert into users(email,name) values ($1,$2) returning id::text`, email, name).Scan(&uid); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`insert into profiles(id,email,name) values ($1::uuid,$2,$3)`, uid, email, name)
	e.exec(`insert into channel_identities(channel,address,user_id,verified_at) values ('imessage',$1,$2::uuid,now())`, phone, uid)
	return uid
}

func (e *e2e) supporter(email, name string) string {
	var uid string
	if err := e.sql.QueryRow(`insert into users(email,name) values ($1,$2) returning id::text`, email, name).Scan(&uid); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`insert into profiles(id,email,name,is_verified_supporter,supporter_status) values ($1::uuid,$2,$3,true,'approved')`, uid, email, name)
	return uid
}

// say sends one inbound text and returns everything sent to that number
// (and to ops) while it was handled.
func (e *e2e) say(phone, text string) []e2eOut {
	e.t.Helper()
	e.seq++
	id := fmt.Sprintf("in-%d-%d", time.Now().UnixNano(), e.seq)
	before := e.fake.count()
	body, _ := json.Marshal(map[string]any{
		"message_handle": id, "content": text, "from_number": phone,
		"to_number": "+12125550000", "is_outbound": false, "service": "iMessage",
		"date_sent": time.Now().UTC().Format(time.RFC3339),
	})
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/channel/sendblue", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("sb-signing-secret", "whsec")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var done bool
		_ = e.sql.QueryRow(`select processed_at is not null from channel_messages where provider_message_id=$1`, id).Scan(&done)
		if done {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(2500 * time.Millisecond) // async replies (a booking's outcome)
	out := e.fake.since(before)
	e.t.Logf("  C> %s", text)
	for _, o := range out {
		who := "H"
		if o.To == e2eOpsNumber {
			who = "OPS"
		} else if o.To != phone {
			who = "to " + o.To
		}
		e.t.Logf("  %s< %s", who, strings.ReplaceAll(o.Text, "\n", "\n      "))
	}
	return out
}

func (e *e2e) post(path string, body any) (int, string) {
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(rb)
}

// waitOut waits for n messages after index i and logs them.
func (e *e2e) waitOut(label string, i, n int) []e2eOut {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && e.fake.count() < i+n {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1 * time.Second)
	out := e.fake.since(i)
	e.t.Logf("[%s] %d message(s) sent:", label, len(out))
	for _, o := range out {
		e.t.Logf("  to %s< %s", o.To, o.Text)
	}
	return out
}

func (e *e2e) opsCancelDropped(taskID string) []e2eOut {
	i := e.fake.count()
	code, body := e.post("/admin/tasks/"+taskID+"/cancel", map[string]string{"cause": "supporter_dropped", "reason": "Supporter had a family emergency and left"})
	e.t.Logf("[ops cancel] %d %s", code, body)
	if code != 200 {
		e.t.Fatalf("cancel: %d %s", code, body)
	}
	return e.waitOut("after ops cancel", i, 1)
}

func (e *e2e) opsReassign(taskID, email string, void bool) []e2eOut {
	i := e.fake.count()
	in := map[string]any{"supporter_email": email}
	if void {
		in["void_prior_work"] = true
		in["reason"] = "Supporter left mid-task (personal reasons)"
	}
	code, body := e.post("/admin/tasks/"+taskID+"/reassign", in)
	e.t.Logf("[ops reassign void=%v] %d %s", void, code, body)
	if code != 200 {
		e.t.Fatalf("reassign: %d %s", code, body)
	}
	return e.waitOut("after ops reassign", i, 1)
}

// accept puts the supporter on the task the way accept does (status stays
// open, assignee set), without calling the accept handler (frozen for review).
func (e *e2e) accept(taskID, supUID, supEmail string) {
	e.exec(`update tasks set assigned_to_id=$2::uuid, assigned_to=$3 where id=$1::uuid`, taskID, supUID, supEmail)
}

func (e *e2e) startWork(taskID, supEmail string) {
	e.exec(`insert into worklogs(task_id,"user",start_at,end_at) values ($1::uuid,$2,now()-interval '20 minutes',now()-interval '5 minutes')`, taskID, supEmail)
}

func (e *e2e) latestTask(uid string) (id, title, status string) {
	_ = e.sql.QueryRow(`select id::text, coalesce(title,''), status from tasks where requester_id=$1::uuid order by created_at desc limit 1`, uid).Scan(&id, &title, &status)
	return
}

func (e *e2e) convState(phone string) string {
	var s, pend string
	_ = e.sql.QueryRow(`select c.state, coalesce(c.pending_draft::text,'') from channel_conversations c join channel_identities i on i.id=c.identity_id where i.address=$1`, phone).Scan(&s, &pend)
	if len(pend) > 300 {
		pend = pend[:300] + "…"
	}
	return s + " " + pend
}

// book books a task by text the normal way and returns its id.
func (e *e2e) book(phone, uid string, request ...string) string {
	e.t.Helper()
	for _, m := range request {
		e.say(phone, m)
	}
	for i := 0; i < 4; i++ {
		if id, _, st := e.latestTask(uid); id != "" && st == "open" {
			return id
		}
		e.say(phone, "YES")
	}
	e.t.Fatalf("no task booked; state=%s", e.convState(phone))
	return ""
}

func TestE2EDropOffMessages(t *testing.T) {
	e := setupE2E(t)
	sup1 := e.supporter("maya@sup.test", "Maya Lopez")
	_ = e.supporter("sam@sup.test", "Sam Chen")

	phone := "+12125550101"
	cust := e.customer(phone, "Alex Rivera")
	t.Log("=== booking by text")
	taskID := e.book(phone, cust, "Can someone pick up my dry cleaning at Hallak Cleaners, 1232 2nd Ave, and bring it to 200 E 70th St tomorrow at 3pm? Budget $40, it's already paid, ticket number 4471.")
	_, title, _ := e.latestTask(cust)
	t.Logf("booked task %s %q; conv=%s", taskID, title, e.convState(phone))
	e.accept(taskID, sup1, "maya@sup.test")

	t.Log("=== ops cancel: supporter dropped")
	out := e.opsCancelDropped(taskID)
	t.Logf("conv after cancel: %s", e.convState(phone))
	if len(out) == 0 {
		t.Errorf("cancel message not delivered")
	}
	for _, m := range strings.Split(os.Getenv("E2E_CANCEL_REPLIES"), "|") {
		if m = strings.TrimSpace(m); m != "" {
			e.say(phone, m)
			t.Logf("  conv=%s", e.convState(phone))
		}
	}

	phone2 := "+12125550102"
	cust2 := e.customer(phone2, "Jordan Lee")
	t.Log("=== booking 2 by text")
	task2 := e.book(phone2, cust2, "Can someone buy milk, eggs and bread at the Whole Foods on 3rd Ave and E 87th St and bring them to 300 E 75th St tomorrow at 11am? Budget $25.")
	e.accept(task2, sup1, "maya@sup.test")
	t.Log("=== ops reassign before start")
	if out := e.opsReassign(task2, "sam@sup.test", false); len(out) == 0 {
		t.Errorf("reassign (before start) message not delivered")
	}
	t.Logf("conv after reassign: %s", e.convState(phone2))

	phone3 := "+12125550103"
	cust3 := e.customer(phone3, "Casey Kim")
	t.Log("=== booking 3 by text")
	task3 := e.book(phone3, cust3, "Can someone wait in line for me at the DMV 11 Greenwich St today, I'll come take their spot. Budget $0, just time.")
	e.accept(task3, sup1, "maya@sup.test")
	e.startWork(task3, "maya@sup.test")
	t.Log("=== ops reassign after start")
	if out := e.opsReassign(task3, "sam@sup.test", true); len(out) == 0 {
		t.Errorf("reassign (after start) message not delivered")
	}
	t.Logf("conv after reassign: %s", e.convState(phone3))
	for _, m := range strings.Split(os.Getenv("E2E_REASSIGN_REPLIES"), "|") {
		if m = strings.TrimSpace(m); m != "" {
			e.say(phone3, m)
			t.Logf("  conv=%s", e.convState(phone3))
		}
	}
}

// clone copies a booked task for a new customer: the errand exactly as the
// chat made it, open, nobody on it, and our booking line for it in the new
// customer's thread (as a text booking leaves one).
func (e *e2e) clone(src, uid, phone string, edit string) string {
	e.t.Helper()
	var id string
	err := e.sql.QueryRow(`
		insert into tasks select (jsonb_populate_record(null::tasks, to_jsonb(t) || jsonb_build_object(
		  'id', gen_random_uuid(), 'requester_id', $2::uuid, 'requester', $3::text, 'assigned_to_id', null,
		  'assigned_to', '', 'status', 'open', 'created_at', now(), 'booking_key', null))).*
		from tasks t where t.id = $1::uuid returning id::text`, src, uid, strings.TrimPrefix(phone, "+")+"@cust.test").Scan(&id)
	if err != nil {
		e.t.Fatalf("clone: %v", err)
	}
	if edit != "" {
		e.exec(`update tasks set `+edit+` where id=$1::uuid`, id)
	}
	e.exec(`insert into channel_messages(channel,provider_message_id,direction,address,body,task_id,received_at,processed_at,created_at)
		values ('imessage',$1,'outbound',$2,'Booked. I''m finding a supporter now…',$3::uuid,now()-interval '1 hour',now()-interval '1 hour',now()-interval '1 hour')`,
		"seed-"+id, phone, id)
	return id
}

type replyCase struct {
	name   string
	kind   string   // cancel | cancel_asap | cancel_companion | reassign_started | reassign_before
	before []string // said before the ops action (a draft left open)
	say    []string
}

func TestE2EReplies(t *testing.T) {
	e := setupE2E(t)
	sup1 := e.supporter("maya@sup.test", "Maya Lopez")
	_ = e.supporter("sam@sup.test", "Sam Chen")

	// Two errands booked by text, the real way: the templates.
	p0 := "+12125550100"
	c0 := e.customer(p0, "Template One")
	tplCancel := e.book(p0, c0, "Can someone pick up my dry cleaning at Hallak Cleaners, 1232 2nd Ave, and bring it to 200 E 70th St tomorrow at 3pm? It's already paid, ticket number 4471.")
	p00 := "+12125550199"
	c00 := e.customer(p00, "Template Two")
	tplGrocery := e.book(p00, c00, "Can someone buy milk, eggs and bread at the Whole Foods on 3rd Ave and E 87th St and bring them to 300 E 75th St tomorrow at 11am? Budget $25.")

	only := os.Getenv("E2E_ONLY")
	cases := []replyCase{
		// ── message 1: cancelled ──
		{name: "en yes (time still ahead)", kind: "cancel", say: []string{"yes", "YES"}},
		{name: "en redo it (asap → time asked)", kind: "cancel_asap", say: []string{"redo it", "tomorrow at 10am", "YES"}},
		{name: "en find someone else, with a time", kind: "cancel_asap", say: []string{"find someone else please, tomorrow at 4pm"}},
		{name: "en grocery redo keeps budget", kind: "cancel_grocery", say: []string{"yes please"}},
		{name: "en no thanks", kind: "cancel", say: []string{"no thanks", "yes"}},
		{name: "en never mind", kind: "cancel", say: []string{"never mind"}},
		{name: "en why", kind: "cancel", say: []string{"why?"}},
		{name: "en what happened", kind: "cancel", say: []string{"what happened to my supporter? are they ok?"}},
		{name: "en was I charged", kind: "cancel", say: []string{"was I charged?"}},
		{name: "en ok", kind: "cancel", say: []string{"ok"}},
		{name: "en unclear", kind: "cancel", say: []string{"this is the second time, I want compensation"}},
		{name: "en new request", kind: "cancel", say: []string{"can someone get me a coffee from Blue Bottle at 54 W 40th St to 2 Bond St now?"}},
		{name: "zh 好的", kind: "cancel", say: []string{"好的"}},
		{name: "zh 再找一个人", kind: "cancel_asap", say: []string{"麻烦再找一个人吧"}},
		{name: "zh 不用了", kind: "cancel", say: []string{"不用了"}},
		{name: "zh 不好", kind: "cancel", say: []string{"不好"}},
		{name: "zh 为什么", kind: "cancel", say: []string{"为什么"}},
		{name: "zh 怎么回事", kind: "cancel", say: []string{"发生了什么？"}},
		{name: "zh 扣钱了吗", kind: "cancel", say: []string{"我被扣钱了吗？"}},
		{name: "es sí", kind: "cancel", say: []string{"sí, por favor"}},
		{name: "es otra persona", kind: "cancel_asap", say: []string{"búscame a otra persona"}},
		{name: "es no gracias", kind: "cancel", say: []string{"no gracias"}},
		{name: "es qué pasó", kind: "cancel", say: []string{"¿qué pasó?"}},
		{name: "es me cobraron", kind: "cancel", say: []string{"¿me cobraron algo?"}},
		{name: "scope: companionship redo while paused", kind: "cancel_companion", say: []string{"yes"}},
		{name: "pending draft older than notice: yes → which", kind: "cancel", before: []string{"Can someone pick up a package at the UPS Store 1636 3rd Ave and bring it to 200 E 70th St tomorrow at 2pm?"}, say: []string{"yes", "REDO"}},
		{name: "pending draft newer than notice: yes books it", kind: "cancel", say: []string{"Can someone pick up a package at the UPS Store 1636 3rd Ave and bring it to 200 E 70th St tomorrow at 2pm?", "yes"}},
		// ── message 2: reassigned ──
		{name: "en ok (after start)", kind: "reassign_started", say: []string{"ok"}},
		{name: "en thanks (before start)", kind: "reassign_before", say: []string{"thanks!"}},
		{name: "en who is it", kind: "reassign_started", say: []string{"who is it?"}},
		{name: "en when will they arrive", kind: "reassign_before", say: []string{"when will they arrive?"}},
		{name: "en charged twice", kind: "reassign_started", say: []string{"will I be charged twice?"}},
		{name: "en cancel it", kind: "reassign_started", say: []string{"cancel it"}},
		{name: "en don't want it anymore", kind: "reassign_before", say: []string{"I don't want it anymore"}},
		{name: "en not a different supporter", kind: "reassign_started", say: []string{"I don't want a different supporter"}},
		{name: "en the original one", kind: "reassign_before", say: []string{"I want the original one"}},
		{name: "en for the supporter", kind: "reassign_started", say: []string{"tell Sam the side door is open, buzz 4B"}},
		{name: "zh 好的谢谢", kind: "reassign_started", say: []string{"好的，谢谢"}},
		{name: "zh 是谁", kind: "reassign_started", say: []string{"新的是谁？"}},
		{name: "zh 什么时候到", kind: "reassign_before", say: []string{"他什么时候到？"}},
		{name: "zh 收两次钱", kind: "reassign_started", say: []string{"会收我两次钱吗？"}},
		{name: "zh 取消", kind: "reassign_started", say: []string{"不要了，取消吧"}},
		{name: "zh 原来那个人", kind: "reassign_before", say: []string{"我要原来那个人"}},
		{name: "es vale gracias", kind: "reassign_started", say: []string{"vale, gracias"}},
		{name: "es quién es", kind: "reassign_started", say: []string{"¿quién es?"}},
		{name: "es cuándo llega", kind: "reassign_before", say: []string{"¿cuándo llega?"}},
		{name: "es dos veces", kind: "reassign_started", say: []string{"¿me van a cobrar dos veces?"}},
		{name: "es cancélalo", kind: "reassign_started", say: []string{"cancélalo, ya no lo quiero"}},
		{name: "es persona de antes", kind: "reassign_before", say: []string{"quiero a la persona de antes"}},
	}
	for i, c := range cases {
		if only != "" && !strings.Contains(c.name, only) {
			continue
		}
		phone := fmt.Sprintf("+1212555%04d", 2000+i)
		uid := e.customer(phone, "Customer "+fmt.Sprint(i))
		t.Logf("\n========== %s", c.name)
		for _, m := range c.before {
			e.say(phone, m)
		}
		var task string
		switch c.kind {
		case "cancel":
			task = e.clone(tplCancel, uid, phone, "")
		case "cancel_asap":
			task = e.clone(tplCancel, uid, phone, "scheduled_at=null, is_immediate=true")
		case "cancel_grocery":
			task = e.clone(tplGrocery, uid, phone, "")
		case "cancel_companion":
			task = e.clone(tplCancel, uid, phone, "category='companionship', title='Walk with me in Central Park', description='Keep me company on a walk in Central Park', location_text='Central Park, 5th Ave and E 72nd St'")
		default:
			task = e.clone(tplGrocery, uid, phone, "")
		}
		e.accept(task, sup1, "maya@sup.test")
		switch c.kind {
		case "reassign_started":
			e.startWork(task, "maya@sup.test")
			e.opsReassign(task, "sam@sup.test", true)
		case "reassign_before":
			e.opsReassign(task, "sam@sup.test", false)
		default:
			e.opsCancelDropped(task)
		}
		before := e.countTasks(uid)
		for _, m := range c.say {
			e.say(phone, m)
		}
		_, _, st := e.latestTask(uid)
		t.Logf("  [end] conv=%s | tasks %d→%d, latest %s", e.convState(phone), before, e.countTasks(uid), st)
	}
}

func (e *e2e) countTasks(uid string) int {
	var n int
	_ = e.sql.QueryRow(`select count(*) from tasks where requester_id=$1::uuid and status<>'cancelled'`, uid).Scan(&n)
	return n
}
