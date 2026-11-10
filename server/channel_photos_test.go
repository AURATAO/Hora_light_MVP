package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel"
)

// Inbound chat photos, server side (channel_photos.go): who sees them on the
// task, the account deletion and the retention sweep, the bucket client and
// the TalkJS upload.

type fakeSigner struct{ signed []string }

func (f *fakeSigner) SignPhoto(_ context.Context, key string, ttl time.Duration) (string, error) {
	if ttl != channel.PhotoLinkTTL {
		return "", errors.New("wrong ttl")
	}
	f.signed = append(f.signed, key)
	return "https://storage.example/sign/" + key + "?token=t", nil
}

type fakePhotoDeleter struct {
	mu      sync.Mutex
	deleted []string
	err     error
}

func (f *fakePhotoDeleter) DeletePhotos(_ context.Context, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, keys...)
	return nil
}

// setupPhotoDB: the deletion fixture with the chat tables and the photo
// table from their migrations; the photo hooks pointed at fakes.
func setupPhotoDB(t *testing.T) (*channel.PGStore, *fakeSigner, *fakePhotoDeleter) {
	t.Helper()
	setupTraceDeletionDB(t, true)
	mustExec(t, `DROP TABLE IF EXISTS public.channel_photos CASCADE`)
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), `DROP TABLE IF EXISTS public.channel_photos CASCADE`); err != nil {
			t.Errorf("drop photos: %v", err)
		}
	})
	b, err := os.ReadFile(filepath.Join("..", "supabase", "migrations", "20261110150000_channel_photos.sql"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, string(b))
	store := &channel.PGStore{DB: sqldb}
	signer, del := &fakeSigner{}, &fakePhotoDeleter{}
	prevStore, prevSigner, prevSweep := chatPhotoStore, chatPhotoSigner, photoSweep
	chatPhotoStore, chatPhotoSigner = store, signer
	photoSweep = func(ctx context.Context) (int, error) { return sweepPhotos(ctx, store, del) }
	t.Cleanup(func() { chatPhotoStore, chatPhotoSigner, photoSweep = prevStore, prevSigner, prevSweep })
	return store, signer, del
}

func seedPhoto(t *testing.T, store *channel.PGStore, identityID, pmid, taskID string) channel.Photo {
	t.Helper()
	p, err := store.SavePhoto(context.Background(), channel.Photo{IdentityID: identityID, Channel: channel.IMessage,
		ProviderMessageID: pmid, ObjectKey: "inbound/" + identityID + "/" + pmid + ".jpg", ContentType: "image/jpeg", SizeBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if taskID != "" {
		if err := store.AttachPhotos(context.Background(), []string{p.ID}, taskID); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func seedIdentity(t *testing.T, userID, addr string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `insert into public.channel_identities (channel, address, user_id, verified_at)
		values ('imessage', $2, $1::uuid, now()) returning id::text`, userID, addr).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The requester's photos on the task: the requester, the supporter on it
// and ops see signed links; a supporter browsing the open board does not.
func TestTaskPhotosOnlyForRequesterAssigneeAndOps(t *testing.T) {
	store, signer, _ := setupPhotoDB(t)
	w := seedOpsWorld(t, "open")
	iid := seedIdentity(t, w.requesterID, "+15550000101")
	seedPhoto(t, store, iid, "pm-label", w.taskID)
	outsider := seedUser(t, "outsider@example.com", "Olive Outsider", true)

	read := func(uid, email string) []any {
		code, rec := callTaskHandler(t, getTask, http.MethodGet, "/tasks/"+w.taskID, w.taskID, uid, email, "", nil)
		if code != http.StatusOK {
			t.Fatalf("getTask as %s: %d %s", email, code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		ps, _ := m["request_photos"].([]any)
		return ps
	}
	for _, who := range []struct{ uid, email string }{{w.requesterID, requesterEmail}, {w.supporterID, oldSupporterEmail}, {w.adminID, adminEmail}} {
		ps := read(who.uid, who.email)
		if len(ps) != 1 {
			t.Fatalf("%s: photos %v", who.email, ps)
		}
		p := ps[0].(map[string]any)
		if !strings.HasPrefix(p["url"].(string), "https://storage.example/sign/inbound/") || p["content_type"] != "image/jpeg" {
			t.Fatalf("%s: %v", who.email, p)
		}
	}
	if ps := read(outsider, "outsider@example.com"); len(ps) != 0 {
		t.Fatalf("a supporter browsing the board sees the photos: %v", ps)
	}
	if len(signer.signed) != 3 {
		t.Fatalf("signed %v", signer.signed)
	}
}

// Account deletion: the photos texted from the account's numbers are
// deleted from the bucket and the table at once; a bucket outage leaves
// them marked, and the sweeper finishes the job.
func TestAccountDeletionDeletesTextedPhotos(t *testing.T) {
	store, _, del := setupPhotoDB(t)
	w := seedOpsWorld(t, "completed")
	iid := seedIdentity(t, w.requesterID, "+15550000202")
	onTask := seedPhoto(t, store, iid, "pm-1", w.taskID)
	loose := seedPhoto(t, store, iid, "pm-2", "")
	other := seedPhoto(t, store, seedIdentity(t, w.supporterID, "+15550000303"), "pm-3", "")

	del.err = errors.New("storage delete returned 503")
	code, body := deleteAs(t, w.requesterID, requesterEmail)
	if code != 200 {
		t.Fatalf("delete: %d %v", code, body)
	}
	var marked int
	_ = db.QueryRow(context.Background(), `select count(*) from public.channel_photos where delete_requested_at is not null and identity_id is null`).Scan(&marked)
	if marked != 2 {
		t.Fatalf("marked %d, want the two photos, unnamed", marked)
	}
	var metaRaw []byte
	if err := db.QueryRow(context.Background(), `select meta from public.audit_logs where action = 'ACCOUNT_DELETED' order by created_at desc limit 1`).Scan(&metaRaw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metaRaw), `"chat_photos_deleted": 2`) && !strings.Contains(string(metaRaw), `"chat_photos_deleted":2`) {
		t.Fatalf("audit meta %s", metaRaw)
	}

	del.err = nil
	n, err := sweepPhotos(context.Background(), store, del)
	if err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if !containsStr(del.deleted, onTask.ObjectKey) || !containsStr(del.deleted, loose.ObjectKey) || containsStr(del.deleted, other.ObjectKey) {
		t.Fatalf("deleted %v", del.deleted)
	}
	var left []string
	rows, err := db.Query(context.Background(), `select id::text from public.channel_photos`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 1 || left[0] != other.ID {
		t.Fatalf("left %v, want only another person's photo", left)
	}
}

// Retention: a photo never booked goes after UnattachedPhotoRetention; one
// on a task stays.
func TestPhotoSweepRetention(t *testing.T) {
	store, _, del := setupPhotoDB(t)
	w := seedOpsWorld(t, "completed")
	iid := seedIdentity(t, w.requesterID, "+15550000404")
	onTask := seedPhoto(t, store, iid, "pm-a", w.taskID)
	old := seedPhoto(t, store, iid, "pm-b", "")
	fresh := seedPhoto(t, store, iid, "pm-c", "")
	mustExec(t, `update public.channel_photos set created_at = now() - interval '8 days' where id in ('`+onTask.ID+`', '`+old.ID+`')`)
	n, err := sweepPhotos(context.Background(), store, del)
	if err != nil || n != 1 || len(del.deleted) != 1 || del.deleted[0] != old.ObjectKey {
		t.Fatalf("sweep %d %v deleted %v", n, err, del.deleted)
	}
	var left int
	_ = db.QueryRow(context.Background(), `select count(*) from public.channel_photos where id in ($1::uuid, $2::uuid)`, onTask.ID, fresh.ID).Scan(&left)
	if left != 2 {
		t.Fatalf("a photo on a task or a fresh one was swept: %d left", left)
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// The bucket client: the service key on every call, links signed through
// the sign endpoint, and no URL in an error.
func TestPhotoBlobsClient(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("apikey") != "svc" || r.Header.Get("Authorization") != "Bearer svc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/storage/v1/object/channel-photos/inbound/i/a.jpg":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/object/channel-photos/inbound/i/a.jpg":
			_, _ = w.Write([]byte("bytes"))
		case r.Method == http.MethodPost && r.URL.Path == "/storage/v1/object/sign/channel-photos/inbound/i/a.jpg":
			var b map[string]int
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["expiresIn"] != 3600 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"signedURL":"/object/sign/channel-photos/inbound/i/a.jpg?token=tk"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/storage/v1/object/channel-photos":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	b := &photoBlobs{base: srv.URL, key: "svc", http: srv.Client()}
	ctx := context.Background()
	if err := b.PutPhoto(ctx, "inbound/i/a.jpg", "image/jpeg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.GetPhoto(ctx, "inbound/i/a.jpg"); err != nil || string(got) != "bytes" {
		t.Fatalf("get %q %v", got, err)
	}
	u, err := b.SignPhoto(ctx, "inbound/i/a.jpg", channel.PhotoLinkTTL)
	if err != nil || u != srv.URL+"/storage/v1/object/sign/channel-photos/inbound/i/a.jpg?token=tk" {
		t.Fatalf("sign %q %v", u, err)
	}
	if err := b.DeletePhotos(ctx, []string{"inbound/i/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := b.PutPhoto(ctx, "inbound/i/other.jpg", "image/jpeg", []byte("x")); err == nil || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("refused upload: %v", err)
	}
	srv.Close()
	if err := b.PutPhoto(ctx, "inbound/i/a.jpg", "image/jpeg", []byte("x")); err == nil || strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "inbound/i") {
		t.Fatalf("down: %v", err)
	}
}

// The photo reaches TalkJS as a file message from the requester.
func TestTalkJSPhotoUpload(t *testing.T) {
	var gotFilename, gotSubtype, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app1/files" || r.Header.Get("Authorization") != "Bearer sk" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = f.Close()
		gotFilename, gotSubtype, gotCT = r.FormValue("filename"), r.FormValue("subtype"), h.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"fileToken":"ft-1"}`))
	}))
	defer srv.Close()
	prev := talkjsAPIBase
	talkjsAPIBase = srv.URL
	defer func() { talkjsAPIBase = prev }()
	t.Setenv("TALKJS_APP_ID", "app1")
	t.Setenv("TALKJS_SECRET_KEY", "sk")
	tok, err := talkjsUploadFile(context.Background(), []byte("jpegbytes"), "image/png")
	if err != nil || tok != "ft-1" || gotFilename != "photo.png" || gotSubtype != "image" || gotCT != "image/png" {
		t.Fatalf("tok %q err %v filename %q subtype %q ct %q", tok, err, gotFilename, gotSubtype, gotCT)
	}
}
