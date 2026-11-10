package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Inbound photos (photos.go) through the router, with an in-memory store,
// bucket and provider.

var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{1}, 200)...)
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{2}, 64)...)
	heicBytes = append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, bytes.Repeat([]byte{3}, 64)...)
	pdfBytes  = []byte("%PDF-1.7\n%âãÏÓ\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")
	mp4Bytes  = append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{4}, 64)...)
)

// mediaAdapter is the rig's sender that can also download attachments.
type mediaAdapter struct {
	*fakeSendAdapter
	media map[string][]byte
	errs  map[string]error
	asked []string
}

func (a *mediaAdapter) FetchMedia(_ context.Context, ref string, max int64) ([]byte, error) {
	a.asked = append(a.asked, ref)
	if err := a.errs[ref]; err != nil {
		return nil, err
	}
	b, ok := a.media[ref]
	if !ok {
		return nil, fmt.Errorf("status 404: %w", ErrMediaUnavailable)
	}
	if int64(len(b)) > max {
		return nil, ErrMediaTooLarge
	}
	return b, nil
}

type memPhotos struct {
	mu     sync.Mutex
	rows   []*memPhotoRow
	nextID int
}

type memPhotoRow struct {
	Photo
	onDraft, relayed bool
}

func (m *memPhotos) SavePhoto(_ context.Context, p Photo) (Photo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.Channel == p.Channel && r.ProviderMessageID == p.ProviderMessageID && r.Position == p.Position {
			return r.Photo, nil
		}
	}
	m.nextID++
	p.ID = fmt.Sprintf("photo-%d", m.nextID)
	p.CreatedAt = time.Now()
	m.rows = append(m.rows, &memPhotoRow{Photo: p})
	return p, nil
}

func (m *memPhotos) loose(r *memPhotoRow, identityID string, since time.Time) bool {
	return r.IdentityID == identityID && r.TaskID == "" && !r.onDraft && !r.relayed && !r.CreatedAt.Before(since)
}

func (m *memPhotos) ClaimLoosePhotos(_ context.Context, identityID string, since time.Time) ([]Photo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Photo
	for _, r := range m.rows {
		if m.loose(r, identityID, since) {
			r.onDraft = true
			out = append(out, r.Photo)
		}
	}
	return out, nil
}

func (m *memPhotos) LoosePhotos(_ context.Context, identityID string, since time.Time) ([]Photo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Photo
	for _, r := range m.rows {
		if m.loose(r, identityID, since) {
			out = append(out, r.Photo)
		}
	}
	return out, nil
}

func (m *memPhotos) each(ids []string, f func(*memPhotoRow)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if contains(ids, r.ID) {
			f(r)
		}
	}
}

func (m *memPhotos) MarkPhotosOnDraft(_ context.Context, ids []string) error {
	m.each(ids, func(r *memPhotoRow) { r.onDraft = true })
	return nil
}

func (m *memPhotos) AttachPhotos(_ context.Context, ids []string, taskID string) error {
	m.each(ids, func(r *memPhotoRow) {
		if r.TaskID == "" {
			r.TaskID = taskID
		}
	})
	return nil
}

func (m *memPhotos) MarkPhotosRelayed(_ context.Context, ids []string, taskID string) error {
	m.each(ids, func(r *memPhotoRow) {
		r.relayed = true
		if r.TaskID == "" {
			r.TaskID = taskID
		}
	})
	return nil
}

func (m *memPhotos) all() []memPhotoRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memPhotoRow, len(m.rows))
	for i, r := range m.rows {
		out[i] = *r
	}
	return out
}

type memBlobs struct {
	mu   sync.Mutex
	objs map[string][]byte
	err  error
}

func (b *memBlobs) PutPhoto(_ context.Context, key, _ string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.objs[key] = append([]byte(nil), data...)
	return nil
}

func (b *memBlobs) GetPhoto(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.objs[key]
	if !ok {
		return nil, errors.New("no object")
	}
	return d, nil
}

// photoChat is the task chat that also takes photos.
type photoChat struct {
	*fakeChat
	photos []string // "task:type:size"
	keys   []string
	err    error
}

func (p *photoChat) PostPhotoAsRequester(_ context.Context, taskID, _ string, data []byte, ct, key string) error {
	if p.err != nil {
		return p.err
	}
	for _, t := range p.active {
		if t.ID == taskID {
			p.photos = append(p.photos, fmt.Sprintf("%s:%s:%d", taskID, ct, len(data)))
			p.keys = append(p.keys, key)
			return nil
		}
	}
	return fmt.Errorf("fake: %w", ErrTaskNotActive)
}

type photoRig struct {
	*rig
	media  *mediaAdapter
	photos *memPhotos
	blobs  *memBlobs
}

func withPhotoRig(g *rig) *photoRig {
	ma := &mediaAdapter{fakeSendAdapter: g.ad, media: map[string][]byte{}, errs: map[string]error{}}
	g.r.Adapters = map[Channel]Adapter{IMessage: ma}
	ps := &memPhotos{}
	bl := &memBlobs{objs: map[string][]byte{}}
	g.r.Photos, g.r.Blobs = ps, bl
	return &photoRig{rig: g, media: ma, photos: ps, blobs: bl}
}

var photoSeq int

// sendPhoto texts one attachment (and optional words); returns our last
// reply.
func (g *photoRig) sendPhoto(t *testing.T, data []byte, text string) string {
	t.Helper()
	photoSeq++
	ref := fmt.Sprintf("https://cdn.sendblue.example/media/%d.jpg?sig=secret", photoSeq)
	if data != nil {
		g.media.media[ref] = data
	}
	before := len(g.ad.sent)
	in := Inbound{Channel: IMessage, ProviderMessageID: fmt.Sprintf("pm-%d", photoSeq), From: me, Text: text, MediaURLs: []string{ref}, ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatalf("photo %q: %v", text, err)
	}
	if len(g.ad.sent) == before {
		return ""
	}
	return g.ad.last()
}

func (g *photoRig) sentSince(n int) []string {
	g.ad.mu.Lock()
	defer g.ad.mu.Unlock()
	var out []string
	for _, o := range g.ad.sent[n:] {
		out = append(out, o.Text)
	}
	return out
}

// ── With a draft open ─────────────────────────────────────────────────────

func TestPhotoDuringDraftIsAttachedConfirmedAndBooked(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	if got := g.sendPhoto(t, jpegBytes, ""); got != CopyPhotoAttached {
		t.Fatalf("reply %q", got)
	}
	p := g.pending(t)
	if len(p.Photos) != 1 || g.state() != StateAwaitingConfirm {
		t.Fatalf("pending photos %v state %s", p.Photos, g.state())
	}
	rows := g.photos.all()
	if len(rows) != 1 || rows[0].ContentType != "image/jpeg" || rows[0].SizeBytes != len(jpegBytes) || !rows[0].onDraft {
		t.Fatalf("rows %+v", rows)
	}
	if !strings.HasPrefix(rows[0].ObjectKey, "inbound/id-"+me+"/") || strings.Contains(rows[0].ObjectKey, "sendblue") {
		t.Fatalf("object key %q", rows[0].ObjectKey)
	}
	if _, ok := g.blobs.objs[rows[0].ObjectKey]; !ok {
		t.Fatal("not in the bucket")
	}
	// A correction shows the summary again, with the photo.
	if sum := g.say(t, "make it 45 minutes"); !strings.Contains(sum, "\n"+CopyPhotoLine+"\n") {
		t.Fatalf("summary without the photo:\n%s", sum)
	}
	if p := g.pending(t); len(p.Photos) != 1 {
		t.Fatalf("re-draft lost the photo: %v", p.Photos)
	}
	if got := g.say(t, "YES"); got != CopyBooked {
		t.Fatalf("yes: %q", got)
	}
	if rows := g.photos.all(); rows[0].TaskID != "task-1" {
		t.Fatalf("photo not on the booked task: %+v", rows[0])
	}
}

func TestPhotoWithWordsDuringDraftConfirmsThenRoutesTheWords(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	n := len(g.ad.sent)
	g.sendPhoto(t, pngBytes, "make it 45 minutes")
	sent := g.sentSince(n)
	if len(sent) != 2 || sent[0] != CopyPhotoAttached || !strings.Contains(sent[1], CopyPhotoLine) || !strings.Contains(sent[1], "Reply YES") {
		t.Fatalf("sent %q", sent)
	}
}

func TestTwoPhotosSayHowMany(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	g.sendPhoto(t, jpegBytes, "")
	g.sendPhoto(t, heicBytes, "")
	if sum := g.say(t, "make it 45 minutes"); !strings.Contains(sum, fmt.Sprintf(CopyPhotosLine, 2)) {
		t.Fatalf("summary:\n%s", sum)
	}
	if rows := g.photos.all(); rows[1].ContentType != "image/heic" {
		t.Fatalf("heic: %+v", rows[1])
	}
}

// Packages & returns asks for the label: a photo answers it and the
// question is not asked again; the other questions still are.
func TestPhotoAnswersTheReturnLabelQuestion(t *testing.T) {
	g := withPhotoRig(newRig(llm.IntentNewTask))
	g.ids.link(me, "user-1")
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	g.r.Playbooks = pbs
	g.parser.draft = llm.TaskDraft{Title: "Amazon return", Category: "errand", Location1: "30 E 14th St, New York, NY 10003"}
	ask := g.say(t, "drop off my Amazon return at the UPS Store at 30 E 14th St")
	if !strings.Contains(ask, "return label or QR code") || !strings.Contains(ask, "boxed") {
		t.Fatalf("asks:\n%s", ask)
	}
	got := g.sendPhoto(t, jpegBytes, "")
	if !strings.HasPrefix(got, CopyPhotoAttached+"\n\n") || strings.Contains(got, "return label") || !strings.Contains(got, "boxed") {
		t.Fatalf("after the photo:\n%s", got)
	}
	p := g.pending(t)
	if contains(p.PlaybookAsk, "label") || !contains(p.PlaybookAsk, "box") || len(p.Photos) != 1 {
		t.Fatalf("asks %v photos %v", p.PlaybookAsk, p.Photos)
	}
	sum := g.say(t, "yes it's boxed and sealed, no receipt")
	if strings.Contains(sum, "return label") || !strings.Contains(sum, CopyPhotoLine) || !strings.Contains(sum, "Reply YES") {
		t.Fatalf("summary:\n%s", sum)
	}
}

// The label photo sent first, with nothing open: the draft that follows
// takes it and does not ask for the label.
func TestLoosePhotoBeforeAReturnSkipsTheLabelQuestion(t *testing.T) {
	g := withPhotoRig(newRig(llm.IntentNewTask))
	g.ids.link(me, "user-1")
	pbs, _ := llm.Playbooks()
	g.r.Playbooks = pbs
	if got := g.sendPhoto(t, jpegBytes, ""); got != CopyPhotoWhatFor {
		t.Fatalf("reply %q", got)
	}
	g.parser.draft = llm.TaskDraft{Title: "Amazon return", Category: "errand", Location1: "30 E 14th St, New York, NY 10003"}
	ask := g.say(t, "drop off my Amazon return at the UPS Store at 30 E 14th St")
	if strings.Contains(ask, "return label") || !strings.Contains(ask, "boxed") {
		t.Fatalf("asks:\n%s", ask)
	}
	if p := g.pending(t); len(p.Photos) != 1 {
		t.Fatalf("photos %v", p.Photos)
	}
}

// ── With a supporter on the job ───────────────────────────────────────────

func photoRelayRig(t *testing.T, active ...ActiveTask) (*photoRig, *photoChat, *fakeTasks) {
	g, chat, tasks := relayRig(llm.IntentOther, active...)
	pc := &photoChat{fakeChat: chat}
	g.r.Chat = pc
	return withPhotoRig(g), pc, tasks
}

func TestPhotoDuringActiveTaskGoesToTheSupporterWithItsWords(t *testing.T) {
	g, chat, tasks := photoRelayRig(t, laundryT)
	n := len(g.ad.sent)
	g.sendPhoto(t, jpegBytes, "leave it by this door please")
	if len(chat.photos) != 1 || chat.photos[0] != fmt.Sprintf("task-1:image/jpeg:%d", len(jpegBytes)) {
		t.Fatalf("photos %v", chat.photos)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:leave it by this door please" {
		t.Fatalf("words %v", chat.posted)
	}
	if len(g.sentSince(n)) != 0 {
		t.Fatalf("echoed: %q", g.sentSince(n))
	}
	if rows := g.photos.all(); rows[0].TaskID != "task-1" || !rows[0].relayed {
		t.Fatalf("row %+v", rows[0])
	}
	if len(tasks.set) != 1 || !strings.HasSuffix(tasks.set[0], "→task-1") {
		t.Fatalf("not tagged: %v", tasks.set)
	}
	// Idempotency key per photo, not the message's own.
	if !strings.HasSuffix(chat.keys[0], "#photo0") {
		t.Fatalf("key %q", chat.keys[0])
	}
	// A photo alone: relayed, no words.
	g.sendPhoto(t, pngBytes, "")
	if len(chat.photos) != 2 || len(chat.posted) != 1 {
		t.Fatalf("photos %v words %v", chat.photos, chat.posted)
	}
}

func TestPhotoRelayFailureIsToldNotSwallowed(t *testing.T) {
	g, chat, _ := photoRelayRig(t, laundryT)
	chat.err = errors.New("talkjs 500")
	if got := g.sendPhoto(t, jpegBytes, "this one"); got != CopyRelayFailed {
		t.Fatalf("reply %q", got)
	}
	if len(chat.posted) != 0 {
		t.Fatal("words went without the photo")
	}
}

func TestPhotoWithSeveralSupportersWaitsForThePick(t *testing.T) {
	g, chat, _ := photoRelayRig(t, laundryT, bagelT)
	list := g.sendPhoto(t, jpegBytes, "")
	if !strings.Contains(list, CopyWhichTask) || len(chat.photos) != 0 || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("list %q photos %v", list, chat.photos)
	}
	g.say(t, "2")
	if len(chat.photos) != 1 || !strings.HasPrefix(chat.photos[0], "task-2:") {
		t.Fatalf("photos %v", chat.photos)
	}
	if len(chat.posted) != 0 {
		t.Fatalf("the marker was posted as words: %v", chat.posted)
	}
	if rows := g.photos.all(); rows[0].TaskID != "task-2" {
		t.Fatalf("row %+v", rows[0])
	}
}

// The words with a photo are scope-checked like any words: refused, and
// nothing reaches the supporter.
func TestPhotoWordsGoThroughTheScopeCheck(t *testing.T) {
	g, chat, _ := photoRelayRig(t, laundryT)
	pbs, _ := llm.Playbooks()
	g.r.Playbooks = pbs
	got := g.sendPhoto(t, jpegBytes, "actually can you give me a massage instead")
	if got != pbs.ScopeRules.Refusal("intimate").Reply {
		t.Fatalf("reply %q", got)
	}
	if len(chat.photos) != 0 || len(chat.posted) != 0 {
		t.Fatalf("relayed: photos %v words %v", chat.photos, chat.posted)
	}
}

// ── With nothing open ─────────────────────────────────────────────────────

func TestPhotoWithNoContextAsksAndJoinsTheNextDraft(t *testing.T) {
	g := withPhotoRig(newRig(llm.IntentNewTask))
	if got := g.sendPhoto(t, jpegBytes, ""); got != CopyPhotoWhatFor {
		t.Fatalf("reply %q", got)
	}
	if g.state() != StateIdle || len(g.photos.all()) != 1 {
		t.Fatalf("state %s rows %d", g.state(), len(g.photos.all()))
	}
	g.say(t, "laundry pickup at 12 Bedford Ave")
	if p := g.pending(t); p == nil || len(p.Photos) != 1 {
		t.Fatalf("pending %+v", p)
	}
}

func TestLoosePhotoOlderThanTheWindowStaysOut(t *testing.T) {
	g := withPhotoRig(newRig(llm.IntentNewTask))
	g.sendPhoto(t, jpegBytes, "")
	g.photos.rows[0].CreatedAt = time.Now().Add(-LoosePhotoWindow - time.Minute)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	if p := g.pending(t); p == nil || len(p.Photos) != 0 {
		t.Fatalf("an old photo joined the draft: %+v", p)
	}
}

func TestPhotoWithAnErrandDraftsWithIt(t *testing.T) {
	g := withPhotoRig(newRig(llm.IntentNewTask))
	g.ids.link(me, "user-1")
	g.sendPhoto(t, jpegBytes, "buy this exact one and bring it to 12 Bedford Ave")
	if p := g.pending(t); p == nil || len(p.Photos) != 1 {
		t.Fatalf("pending %+v", p)
	}
	if sum := g.say(t, "$20"); !strings.Contains(sum, CopyPhotoLine) {
		t.Fatalf("summary:\n%s", sum)
	}
}

// ── What is refused ───────────────────────────────────────────────────────

func TestNonImageFileIsRefusedPolitely(t *testing.T) {
	for _, data := range [][]byte{pdfBytes, mp4Bytes, []byte("just some text")} {
		g := withPhotoRig(linkedRig(t))
		if got := g.sendPhoto(t, data, ""); got != CopyPhotoNotImage {
			t.Fatalf("reply %q", got)
		}
		if len(g.photos.all()) != 0 || len(g.blobs.objs) != 0 || len(g.pending(t).Photos) != 0 {
			t.Fatal("a non-image was kept")
		}
	}
}

func TestExpiredOrFailedDownloadAsksAgain(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	// No bytes behind the link: the provider answers 404 (expired).
	if got := g.sendPhoto(t, nil, ""); got != CopyPhotoFailed {
		t.Fatalf("expired: %q", got)
	}
	// The bucket refuses the upload.
	g.blobs.err = errors.New("storage upload returned 503")
	if got := g.sendPhoto(t, jpegBytes, ""); got != CopyPhotoFailed {
		t.Fatalf("upload failed: %q", got)
	}
	if len(g.photos.all()) != 0 {
		t.Fatal("a row without an object")
	}
	// With words: the problem, then the words as usual.
	g.blobs.err = nil
	n := len(g.ad.sent)
	g.sendPhoto(t, nil, "make it 45 minutes")
	if s := g.sentSince(n); len(s) != 2 || s[0] != CopyPhotoFailed || !strings.Contains(s[1], "Reply YES") {
		t.Fatalf("sent %q", s)
	}
}

func TestTooLargePhotoIsRefused(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	big := append(append([]byte(nil), jpegBytes...), make([]byte, MaxPhotoBytes)...)
	if got := g.sendPhoto(t, big, ""); got != CopyPhotoTooLarge {
		t.Fatalf("reply %q", got)
	}
}

// Photos off (no store): the old answer, nothing downloaded.
func TestPhotosOffKeepsTheTextOnlyAnswer(t *testing.T) {
	g := withPhotoRig(linkedRig(t))
	g.r.Photos = nil
	if got := g.sendPhoto(t, jpegBytes, ""); got != CopyTextOnly {
		t.Fatalf("reply %q", got)
	}
	if len(g.media.asked) != 0 {
		t.Fatal("downloaded with photos off")
	}
}

func TestSniffPhotoType(t *testing.T) {
	for _, c := range []struct {
		data []byte
		want string
	}{
		{jpegBytes, "image/jpeg"}, {pngBytes, "image/png"}, {heicBytes, "image/heic"},
		{[]byte("GIF89a......"), "image/gif"}, {append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 20)...), "image/webp"},
		{append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'i', 'f', '1'}, make([]byte, 8)...), "image/heif"},
		{pdfBytes, ""}, {mp4Bytes, ""}, {nil, ""}, {[]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), ""},
	} {
		if got := SniffPhotoType(c.data); got != c.want {
			t.Errorf("%q: %q, want %q", c.data[:min(len(c.data), 12)], got, c.want)
		}
	}
}

// ── Adapters ──────────────────────────────────────────────────────────────

func TestSendblueFetchMediaNeverLeaksTheLink(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.jpg":
			_, _ = w.Write(jpegBytes)
		case "/big.jpg":
			_, _ = w.Write(make([]byte, 2048))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &SendblueAdapter{HTTP: srv.Client()}
	got, err := a.FetchMedia(context.Background(), srv.URL+"/ok.jpg?sig=s3cret", 1024)
	if err != nil || !bytes.Equal(got, jpegBytes) {
		t.Fatalf("ok: %v", err)
	}
	if _, err := a.FetchMedia(context.Background(), srv.URL+"/big.jpg", 1024); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("big: %v", err)
	}
	_, err = a.FetchMedia(context.Background(), srv.URL+"/gone.jpg?sig=s3cret", 1024)
	if !errors.Is(err, ErrMediaUnavailable) || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("gone: %v", err)
	}
	// A transport failure: net/http's own error names the URL; ours must not.
	srv.Close()
	_, err = a.FetchMedia(context.Background(), srv.URL+"/ok.jpg?sig=s3cret", 1024)
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "http") {
		t.Fatalf("down: %v", err)
	}
	if _, err := a.FetchMedia(context.Background(), "http://plain.example/x.jpg", 1024); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("http link: %v", err)
	}
}

func TestWhatsAppImageParsedAndFetched(t *testing.T) {
	body := `{"entry":[{"changes":[{"field":"messages","value":{"metadata":{"display_phone_number":"15550001111"},
		"messages":[{"from":"15551234567","id":"wamid.1","timestamp":"1700000000","type":"image","image":{"id":"MEDIA1","mime_type":"image/jpeg","caption":"this is the label"}},
		{"from":"15551234567","id":"wamid.2","timestamp":"1700000001","type":"document","document":{"id":"DOC1","mime_type":"application/pdf","filename":"label.pdf"}}]}}]}]}`
	a := &WhatsAppAdapter{}
	got, err := a.ParseInbound([]byte(body))
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Text != "this is the label" || len(got[0].MediaURLs) != 1 || got[0].MediaURLs[0] != "MEDIA1" {
		t.Fatalf("image: %+v", got[0])
	}
	if got[1].Text != "" || len(got[1].MediaURLs) != 1 || got[1].MediaURLs[0] != "DOC1" {
		t.Fatalf("document: %+v", got[1])
	}

	var srvURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v21.0/MEDIA1":
			_ = json.NewEncoder(w).Encode(map[string]any{"url": srvURL + "/dl/1?token=abc", "file_size": len(jpegBytes)})
		case "/v21.0/HUGE":
			_ = json.NewEncoder(w).Encode(map[string]any{"url": srvURL + "/dl/2", "file_size": 50 << 20})
		case "/dl/1":
			_, _ = w.Write(jpegBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a = &WhatsAppAdapter{AccessToken: "tok", BaseURL: srv.URL, HTTP: srv.Client()}
	data, err := a.FetchMedia(context.Background(), "MEDIA1", MaxPhotoBytes)
	if err != nil || !bytes.Equal(data, jpegBytes) {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := a.FetchMedia(context.Background(), "HUGE", MaxPhotoBytes); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("huge: %v", err)
	}
	if _, err := a.FetchMedia(context.Background(), "EXPIRED", MaxPhotoBytes); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("expired: %v", err)
	}
}
