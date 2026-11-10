package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// Inbound photos (customer → supporter). A requester texts a photo — a
// return label or QR code, the item to buy, where to leave something. Until
// this file the router only used MediaURLs to skip empty messages, so the
// photo was dropped.
//
// THE RULES
//   - Downloaded at once: provider media links expire (Sendblue's CDN links,
//     Meta's media URLs within minutes) and must not be kept as references.
//   - Images only (JPEG, PNG, GIF, WebP, HEIC/HEIF), judged by the bytes,
//     never by the provider's word or a file name; at most MaxPhotoBytes. A
//     PDF, a video, a voice note is refused politely.
//   - Stored in the PRIVATE bucket channel-photos (migration
//     20261110150000_channel_photos.sql) under inbound/<identity>/<random>,
//     and handed out only as short-lived signed links, from handlers that
//     have already decided the caller may see them (the task's requester or
//     assignee, ops).
//   - Never logged: no provider URL, no signed URL, no bytes. Logs and
//     traces carry the row id, the size, the type and the outcome only.
//   - Not read by a model. The words that come with a photo go through the
//     scope check like any other words; the picture does not.
//
// WHERE A PHOTO GOES (routePhotos)
//   - a draft is open: onto the draft ("Got the photo — your supporter will
//     see it."), "Photo attached" in the summary, onto the task at booking.
//     A playbook question a photo answers (the return label) is answered.
//   - a supporter is on the job: into the task chat (TalkJS), with the
//     words that came with it, and onto the task.
//   - neither: kept, and the person is asked what it is for; the next draft
//     started within LoosePhotoWindow takes it.
//
// RETENTION (sweepPhotos in package main): a photo on a task lives as long
// as the task's photos do and goes with the requester's account; a photo
// never booked is deleted UnattachedPhotoRetention after it arrived.

// MaxPhotoBytes is the largest photo kept (10 MB — a phone photo is 2–5 MB).
// The bucket's own file_size_limit is the same figure.
const MaxPhotoBytes = 10 << 20

// MaxPhotosPerMessage / MaxDraftPhotos bound what one message and one draft
// carry.
const (
	MaxPhotosPerMessage = 5
	MaxDraftPhotos      = 5
)

// LoosePhotoWindow: a photo sent with nothing open is attached to a draft
// the person starts within this long after it.
const LoosePhotoWindow = 30 * time.Minute

// UnattachedPhotoRetention: a photo that never reached a task is deleted
// this long after it arrived (the draft it was on was dropped or never
// booked).
const UnattachedPhotoRetention = 7 * 24 * time.Hour

// PhotoLinkTTL: how long a signed link to a photo stays valid.
const PhotoLinkTTL = time.Hour

// photoTypes are the image types kept, with the object key's extension.
var photoTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"image/heic": ".heic",
	"image/heif": ".heif",
}

// MediaFetcher is an adapter's way to download one inbound attachment: ref
// is what ParseInbound put in Inbound.MediaURLs (Sendblue's media URL, a
// WhatsApp media id). At most max bytes are read; more is ErrMediaTooLarge.
// Errors never carry the reference (it may be a bearer link).
type MediaFetcher interface {
	FetchMedia(ctx context.Context, ref string, max int64) ([]byte, error)
}

var (
	// ErrMediaTooLarge: the attachment is over the limit.
	ErrMediaTooLarge = errors.New("channel: media too large")
	// ErrMediaUnavailable: the provider would not give the attachment (an
	// expired link, a 404, a refused download).
	ErrMediaUnavailable = errors.New("channel: media unavailable")
)

// Photo is one stored inbound photo (channel_photos).
type Photo struct {
	ID                string
	IdentityID        string
	Channel           Channel
	ProviderMessageID string
	Position          int
	ObjectKey         string
	ContentType       string
	SizeBytes         int
	TaskID            string
	CreatedAt         time.Time
}

// PhotoStore keeps the photo rows. PGStore implements it.
type PhotoStore interface {
	// SavePhoto records a stored photo; a retry of the same message and
	// position returns the row already there.
	SavePhoto(ctx context.Context, p Photo) (Photo, error)
	// ClaimLoosePhotos marks the identity's loose photos (no draft, no task,
	// not relayed) received since `since` as on a draft, and returns them.
	ClaimLoosePhotos(ctx context.Context, identityID string, since time.Time) ([]Photo, error)
	// LoosePhotos lists them without claiming.
	LoosePhotos(ctx context.Context, identityID string, since time.Time) ([]Photo, error)
	// MarkPhotosOnDraft marks photos as on a draft (no longer loose).
	MarkPhotosOnDraft(ctx context.Context, ids []string) error
	// AttachPhotos puts photos on a booked task — only those not on a task
	// yet, so a retried booking is a no-op.
	AttachPhotos(ctx context.Context, ids []string, taskID string) error
	// MarkPhotosRelayed records photos passed into a task's chat (and puts
	// them on that task).
	MarkPhotosRelayed(ctx context.Context, ids []string, taskID string) error
}

// PhotoBlobs is the private bucket. Implemented in package main over
// Supabase Storage with the service key.
type PhotoBlobs interface {
	PutPhoto(ctx context.Context, key, contentType string, data []byte) error
	GetPhoto(ctx context.Context, key string) ([]byte, error)
}

// PhotoChat is the task chat's photo capability (talkjsRelay): the photo as
// a file message from the requester. Optional on TaskChat.
type PhotoChat interface {
	PostPhotoAsRequester(ctx context.Context, taskID, userID string, data []byte, contentType, key string) error
}

// photosOn: inbound photos are handled (store and bucket configured).
func (r *Router) photosOn() bool { return r.Photos != nil && r.Blobs != nil }

// inboundPhoto is a photo of the message being routed, with its bytes (kept
// in memory for the relay, never anywhere else).
type inboundPhoto struct {
	Photo
	data []byte
}

type photosKey struct{}

func withPhotos(ctx context.Context, ps []inboundPhoto) context.Context {
	return context.WithValue(ctx, photosKey{}, ps)
}

func photosFrom(ctx context.Context) []inboundPhoto {
	ps, _ := ctx.Value(photosKey{}).([]inboundPhoto)
	return ps
}

func photoIDs(ps []inboundPhoto) []string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

// photoOutcome names what happened to one attachment.
type photoOutcome string

const (
	photoSaved    photoOutcome = "saved"
	photoNotImage photoOutcome = "not_image"
	photoTooLarge photoOutcome = "too_large"
	photoFailed   photoOutcome = "failed"
)

// ingestResult: the photos kept, and the worst reason one was not.
type ingestResult struct {
	saved                      []inboundPhoto
	notImage, tooLarge, failed int
}

// problemCopy is the reply for attachments none of which could be kept.
func (res ingestResult) problemCopy() string {
	switch {
	case res.failed > 0:
		return CopyPhotoFailed
	case res.tooLarge > 0:
		return CopyPhotoTooLarge
	case res.notImage > 0:
		return CopyPhotoNotImage
	}
	return ""
}

// ingestPhotos downloads, checks and stores every attachment of in. One
// span per attachment (size, type, latency, outcome; never a URL).
func (r *Router) ingestPhotos(ctx context.Context, in Inbound, id Identity) ingestResult {
	var res ingestResult
	fetcher, _ := r.Adapters[in.Channel].(MediaFetcher)
	for i, ref := range in.MediaURLs {
		if i >= MaxPhotosPerMessage {
			log.Printf("[channel] photo id=%s: %d attachments, only the first %d kept", in.ProviderMessageID, len(in.MediaURLs), MaxPhotosPerMessage)
			break
		}
		start := time.Now()
		sctx, sp := tracing.StartSpan(ctx, "channel.photo_saved", tracing.KindTool)
		p, outcome, err := r.ingestOne(sctx, fetcher, in, id, i, ref)
		sp.Attr(attribute.String("hora.photo.outcome", string(outcome)),
			attribute.Int("hora.photo.size_bytes", p.SizeBytes),
			attribute.String("hora.photo.content_type", p.ContentType),
			attribute.Int64("hora.photo.latency_ms", time.Since(start).Milliseconds()))
		sp.Fail(err)
		sp.End()
		log.Printf("[channel] photo id=%s pos=%d identity=%s outcome=%s type=%s size=%d latency=%s err=%v",
			in.ProviderMessageID, i, id.ID, outcome, p.ContentType, p.SizeBytes, time.Since(start).Round(time.Millisecond), err)
		switch outcome {
		case photoSaved:
			res.saved = append(res.saved, p)
		case photoNotImage:
			res.notImage++
		case photoTooLarge:
			res.tooLarge++
		default:
			res.failed++
		}
	}
	return res
}

func (r *Router) ingestOne(ctx context.Context, fetcher MediaFetcher, in Inbound, id Identity, pos int, ref string) (inboundPhoto, photoOutcome, error) {
	if fetcher == nil {
		return inboundPhoto{}, photoFailed, fmt.Errorf("photo: %s adapter cannot download media", in.Channel)
	}
	data, err := fetcher.FetchMedia(ctx, ref, MaxPhotoBytes)
	if errors.Is(err, ErrMediaTooLarge) {
		return inboundPhoto{}, photoTooLarge, nil
	}
	if err != nil {
		return inboundPhoto{}, photoFailed, fmt.Errorf("photo download: %w", err)
	}
	ct := SniffPhotoType(data)
	if ct == "" {
		return inboundPhoto{Photo: Photo{SizeBytes: len(data)}}, photoNotImage, nil
	}
	p := Photo{IdentityID: id.ID, Channel: in.Channel, ProviderMessageID: in.ProviderMessageID, Position: pos,
		ObjectKey: "inbound/" + id.ID + "/" + randomHex(16) + photoTypes[ct], ContentType: ct, SizeBytes: len(data)}
	if err := r.Blobs.PutPhoto(ctx, p.ObjectKey, ct, data); err != nil {
		return inboundPhoto{Photo: p}, photoFailed, fmt.Errorf("photo upload: %w", err)
	}
	saved, err := r.Photos.SavePhoto(ctx, p)
	if err != nil {
		return inboundPhoto{Photo: p}, photoFailed, fmt.Errorf("photo record: %w", err)
	}
	return inboundPhoto{Photo: saved, data: data}, photoSaved, nil
}

// SniffPhotoType is the image type of data by its first bytes, "" when it
// is not one of the types kept.
func SniffPhotoType(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// ISO base media (HEIC/HEIF): "ftyp" at offset 4, then the brand.
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx":
			return "image/heic"
		case "mif1", "msf1", "heif":
			return "image/heif"
		}
		return "" // an MP4/MOV video has the same box
	}
	ct := http.DetectContentType(data)
	if _, ok := photoTypes[ct]; ok {
		return ct
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("random: %v", err))
	}
	return hex.EncodeToString(b)
}

// mediaErr makes an HTTP client error safe to log: net/http puts the URL in
// the message ("Get \"https://…\": …"), and a media URL is a bearer link.
func mediaErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

// readMedia reads at most max bytes of a media download; more is
// ErrMediaTooLarge.
func readMedia(resp *http.Response, max int64) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %w", resp.StatusCode, ErrMediaUnavailable)
	}
	if resp.ContentLength > max {
		return nil, ErrMediaTooLarge
	}
	var buf bytes.Buffer
	n, err := buf.ReadFrom(http.MaxBytesReader(nil, resp.Body, max+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, ErrMediaTooLarge
		}
		return nil, fmt.Errorf("read media: %w", mediaErr(err))
	}
	if n > max {
		return nil, ErrMediaTooLarge
	}
	return buf.Bytes(), nil
}

// ── Routing ────────────────────────────────────────────────────────────────

// photoDraftStates: states where an open draft takes a photo.
func photoDraftState(s State) bool {
	switch s {
	case StateAwaitingConfirm, StateAwaitingName, StateAwaitingBudget, StateAwaitingSetup:
		return true
	}
	return false
}

// routePhotos places the message's photos (see the file comment). handled:
// the message is answered; otherwise its words go on through the router with
// conv as returned (the draft now carries the photos).
func (r *Router) routePhotos(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, Conversation, error) {
	ps := photosFrom(ctx)
	if len(ps) == 0 {
		return false, conv, nil
	}
	// 1. A draft is open: the photo goes on it.
	if conv.Pending != nil && photoDraftState(conv.State) {
		p := conv.Pending
		for _, ph := range ps {
			if len(p.Photos) < MaxDraftPhotos && !contains(p.Photos, ph.ID) {
				p.Photos = append(p.Photos, ph.ID)
			}
		}
		if err := r.Photos.MarkPhotosOnDraft(ctx, photoIDs(ps)); err != nil {
			return true, conv, fmt.Errorf("routePhotos: %w", err)
		}
		answered := r.answerPhotoAsks(p)
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return true, conv, fmt.Errorf("routePhotos: %w", err)
		}
		log.Printf("[channel] photo identity=%s: %d on the draft (answered a photo question: %t)", id.ID, len(p.Photos), answered)
		if text != "" {
			// The line, then the words as any other words for the draft.
			if err := r.Reply(ctx, in, CopyPhotoAttached, ""); err != nil {
				return true, conv, err
			}
			return false, conv, nil
		}
		if answered && conv.State == StateAwaitingConfirm {
			if asks := r.pendingAsks(p); len(asks) > 0 {
				return true, conv, r.Reply(ctx, in, CopyPhotoAttached+"\n\n"+playbookAsksText(asks), "")
			}
			if err := r.Reply(ctx, in, CopyPhotoAttached, ""); err != nil {
				return true, conv, err
			}
			return true, conv, r.afterAsks(ctx, in, id, conv)
		}
		return true, conv, r.Reply(ctx, in, CopyPhotoAttached, "")
	}
	// 2. A supporter is on the job: into the task chat.
	if id.Linked() && r.Chat != nil && conv.State != StateAwaitingTaskChoice {
		active, err := r.Chat.ActiveTasks(ctx, id.UserID)
		if err != nil {
			return true, conv, fmt.Errorf("routePhotos: %w", err)
		}
		switch len(active) {
		case 0:
		case 1:
			return true, conv, r.relayPhotos(ctx, in, id, active[0].ID, ps, text)
		default:
			// Never guessed: which supporter? The photos wait (loose) and
			// go with the pick (deliverHold).
			held := text
			if held == "" {
				held = heldPhotoMarker
			}
			return true, conv, r.askWhichTask(ctx, in, conv, active, held)
		}
	}
	// 3. Nothing open: kept; the next draft within LoosePhotoWindow takes
	// it. With words, the words are routed (an errand drafts and takes it).
	if text != "" {
		return false, conv, nil
	}
	return true, conv, r.Reply(ctx, in, CopyPhotoWhatFor, "")
}

// answerPhotoAsks drops the draft's open questions a photo answers (the
// return label or QR code). True when one was open.
func (r *Router) answerPhotoAsks(p *PendingDraft) bool {
	if len(p.PlaybookAsk) == 0 || r.Playbooks == nil {
		return false
	}
	photoAsk := map[string]bool{}
	for _, a := range r.Playbooks.AsksOf(r.Playbooks.ByID(p.Playbook)) {
		if a.Photo {
			photoAsk[a.Key] = true
		}
	}
	var left []string
	answered := false
	for _, k := range p.PlaybookAsk {
		if photoAsk[k] {
			answered = true
			continue
		}
		left = append(left, k)
	}
	p.PlaybookAsk = left
	return answered
}

// heldPhotoMarker is the held relay text for a photo sent with no words
// while asking which supporter it is for; never posted as words.
const heldPhotoMarker = "(photo)"

// relayPhotos posts the photos into the task's chat as the requester, then
// the words that came with them. The requester gets no echo, as with words.
func (r *Router) relayPhotos(ctx context.Context, in Inbound, id Identity, taskID string, ps []inboundPhoto, text string) error {
	if ok, err := r.postPhotos(ctx, in, id, taskID, ps); !ok {
		return err
	}
	if text != "" {
		_, err := r.relaySent(ctx, in, id, taskID, text)
		return err
	}
	r.noteRelayed(ctx, in, id, taskID, "")
	return nil
}

// postPhotos sends each photo to the task chat and puts it on the task. On
// failure the person has been told (ok=false).
func (r *Router) postPhotos(ctx context.Context, in Inbound, id Identity, taskID string, ps []inboundPhoto) (bool, error) {
	pc, canPost := r.Chat.(PhotoChat)
	var sent []string
	for _, p := range ps {
		if !canPost {
			break
		}
		start := time.Now()
		sctx, sp := tracing.StartSpan(ctx, "channel.photo_relayed", tracing.KindTool)
		err := pc.PostPhotoAsRequester(sctx, taskID, id.UserID, p.data, p.ContentType, fmt.Sprintf("%s#photo%d", p.ProviderMessageID, p.Position))
		sp.Attr(attribute.String("hora.task_id", taskID), attribute.Int("hora.photo.size_bytes", p.SizeBytes),
			attribute.String("hora.photo.content_type", p.ContentType),
			attribute.Int64("hora.photo.latency_ms", time.Since(start).Milliseconds()))
		sp.Fail(err)
		sp.End()
		if err != nil {
			log.Printf("[channel] photo relay identity=%s task=%s photo=%s: %v", id.ID, taskID, p.ID, err)
			if errors.Is(err, ErrTaskNotActive) {
				return false, r.Reply(ctx, in, CopyRelayGone, "")
			}
			return false, r.Reply(ctx, in, CopyRelayFailed, taskID)
		}
		sent = append(sent, p.ID)
		log.Printf("[channel] photo relayed identity=%s task=%s photo=%s type=%s size=%d", id.ID, taskID, p.ID, p.ContentType, p.SizeBytes)
	}
	// On the task either way: the supporter sees it on the task screen.
	if err := r.Photos.MarkPhotosRelayed(ctx, photoIDs(ps), taskID); err != nil {
		log.Printf("[channel] photo relay identity=%s task=%s: record: %v", id.ID, taskID, err)
	}
	if !canPost {
		log.Printf("[channel] photo relay identity=%s task=%s: chat cannot carry photos; on the task only", id.ID, taskID)
	}
	return true, nil
}

// relayLoosePhotos sends the identity's loose photos (held while asking
// which supporter) to taskID. Best effort: the words still go.
func (r *Router) relayLoosePhotos(ctx context.Context, in Inbound, id Identity, taskID string) bool {
	if !r.photosOn() {
		return true
	}
	loose, err := r.Photos.LoosePhotos(ctx, id.ID, r.now().Add(-LoosePhotoWindow))
	if err != nil {
		log.Printf("[channel] photo hold identity=%s: %v", id.ID, err)
		return true
	}
	var ps []inboundPhoto
	for _, p := range loose {
		data, err := r.Blobs.GetPhoto(ctx, p.ObjectKey)
		if err != nil {
			log.Printf("[channel] photo hold identity=%s photo=%s: read: %v", id.ID, p.ID, err)
			continue
		}
		ps = append(ps, inboundPhoto{Photo: p, data: data})
	}
	if len(ps) == 0 {
		return true
	}
	ok, err := r.postPhotos(ctx, in, id, taskID, ps)
	if err != nil {
		log.Printf("[channel] photo hold identity=%s: %v", id.ID, err)
	}
	return ok
}

// deliverHold sends a held relay to taskID: the photos held with it, then
// the words (the photo marker is not words).
func (r *Router) deliverHold(ctx context.Context, in Inbound, id Identity, taskID, held string) error {
	words := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(held), heldPhotoMarker))
	hadPhotos := strings.HasPrefix(strings.TrimSpace(held), heldPhotoMarker)
	r.relayLoosePhotos(ctx, in, id, taskID)
	if words == "" && hadPhotos {
		return nil
	}
	return r.Chat.PostAsRequester(ctx, taskID, id.UserID, capText(words), in.ProviderMessageID)
}

// claimDraftPhotos puts the person's loose photos on a new draft.
func (r *Router) claimDraftPhotos(ctx context.Context, id Identity, p *PendingDraft) {
	if !r.photosOn() {
		return
	}
	loose, err := r.Photos.ClaimLoosePhotos(ctx, id.ID, r.now().Add(-LoosePhotoWindow))
	if err != nil {
		log.Printf("[channel] photo claim identity=%s: %v", id.ID, err)
		return
	}
	for _, ph := range loose {
		if len(p.Photos) < MaxDraftPhotos && !contains(p.Photos, ph.ID) {
			p.Photos = append(p.Photos, ph.ID)
		}
	}
	if len(loose) > 0 {
		log.Printf("[channel] photo identity=%s: %d loose photo(s) put on the new draft", id.ID, len(loose))
	}
}

// attachBookedPhotos puts the draft's photos on the task it booked.
func (r *Router) attachBookedPhotos(ctx context.Context, id Identity, p *PendingDraft, taskID string) {
	if p == nil || len(p.Photos) == 0 || taskID == "" || r.Photos == nil {
		return
	}
	if err := r.Photos.AttachPhotos(ctx, p.Photos, taskID); err != nil {
		log.Printf("[channel][ERROR] photo attach identity=%s task=%s: %v", id.ID, taskID, err)
		return
	}
	log.Printf("[channel] photo identity=%s task=%s: %d attached", id.ID, taskID, len(p.Photos))
}

// ── Postgres ───────────────────────────────────────────────────────────────

const photoCols = `id::text, coalesce(identity_id::text, ''), channel, provider_message_id, position, object_key, content_type, size_bytes, coalesce(task_id::text, ''), created_at`

func scanPhotos(rows *sql.Rows) ([]Photo, error) {
	defer func() {
		if err := rows.Close(); err != nil {
			log.Printf("[channel] photos: close rows: %v", err)
		}
	}()
	var out []Photo
	for rows.Next() {
		var p Photo
		var ch string
		if err := rows.Scan(&p.ID, &p.IdentityID, &ch, &p.ProviderMessageID, &p.Position, &p.ObjectKey, &p.ContentType, &p.SizeBytes, &p.TaskID, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan photo: %w", err)
		}
		p.Channel = Channel(ch)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PGStore) SavePhoto(ctx context.Context, p Photo) (Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `
		WITH ins AS (
		  INSERT INTO channel_photos (identity_id, channel, provider_message_id, position, object_key, content_type, size_bytes)
		  VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		  ON CONFLICT (channel, provider_message_id, position) DO NOTHING
		  RETURNING `+photoCols+`)
		SELECT * FROM ins
		UNION ALL
		SELECT `+photoCols+` FROM channel_photos
		 WHERE channel = $2 AND provider_message_id = $3 AND position = $4 AND NOT EXISTS (SELECT 1 FROM ins)`,
		p.IdentityID, string(p.Channel), p.ProviderMessageID, p.Position, p.ObjectKey, p.ContentType, p.SizeBytes)
	if err != nil {
		return Photo{}, fmt.Errorf("save photo: %w", err)
	}
	got, err := scanPhotos(rows)
	if err != nil {
		return Photo{}, fmt.Errorf("save photo: %w", err)
	}
	if len(got) == 0 {
		return Photo{}, fmt.Errorf("save photo: no row")
	}
	return got[0], nil
}

const loosePhoto = `identity_id = $1::uuid AND task_id IS NULL AND on_draft_at IS NULL AND relayed_at IS NULL
	AND delete_requested_at IS NULL AND created_at >= $2`

func (s *PGStore) ClaimLoosePhotos(ctx context.Context, identityID string, since time.Time) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `UPDATE channel_photos SET on_draft_at = now() WHERE `+loosePhoto+`
		RETURNING `+photoCols, identityID, since)
	if err != nil {
		return nil, fmt.Errorf("claim loose photos: %w", err)
	}
	return scanPhotos(rows)
}

func (s *PGStore) LoosePhotos(ctx context.Context, identityID string, since time.Time) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+photoCols+` FROM channel_photos WHERE `+loosePhoto+`
		ORDER BY created_at, position`, identityID, since)
	if err != nil {
		return nil, fmt.Errorf("loose photos: %w", err)
	}
	return scanPhotos(rows)
}

func (s *PGStore) MarkPhotosOnDraft(ctx context.Context, ids []string) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE channel_photos SET on_draft_at = coalesce(on_draft_at, now())
		WHERE id = ANY($1::uuid[])`, pgUUIDArray(ids)); err != nil {
		return fmt.Errorf("mark photos on draft: %w", err)
	}
	return nil
}

func (s *PGStore) AttachPhotos(ctx context.Context, ids []string, taskID string) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE channel_photos SET task_id = $2::uuid
		WHERE id = ANY($1::uuid[]) AND task_id IS NULL AND delete_requested_at IS NULL`, pgUUIDArray(ids), taskID); err != nil {
		return fmt.Errorf("attach photos: %w", err)
	}
	return nil
}

func (s *PGStore) MarkPhotosRelayed(ctx context.Context, ids []string, taskID string) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE channel_photos SET task_id = coalesce(task_id, $2::uuid), relayed_at = now()
		WHERE id = ANY($1::uuid[]) AND delete_requested_at IS NULL`, pgUUIDArray(ids), taskID); err != nil {
		return fmt.Errorf("mark photos relayed: %w", err)
	}
	return nil
}

// TaskPhotos: the photos on a task, oldest first (GET /tasks/:id).
func (s *PGStore) TaskPhotos(ctx context.Context, taskID string) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+photoCols+` FROM channel_photos
		WHERE task_id = $1::uuid AND delete_requested_at IS NULL ORDER BY created_at, position`, taskID)
	if err != nil {
		return nil, fmt.Errorf("task photos: %w", err)
	}
	return scanPhotos(rows)
}

// MessagePhotos: the photos of these inbound messages, by provider id (Ops
// Chats).
func (s *PGStore) MessagePhotos(ctx context.Context, ch Channel, providerIDs []string) (map[string][]Photo, error) {
	out := map[string][]Photo{}
	if len(providerIDs) == 0 {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+photoCols+` FROM channel_photos
		WHERE channel = $1 AND provider_message_id = ANY($2::text[]) AND delete_requested_at IS NULL
		ORDER BY position`, string(ch), pgTextArray(providerIDs))
	if err != nil {
		return nil, fmt.Errorf("message photos: %w", err)
	}
	ps, err := scanPhotos(rows)
	if err != nil {
		return nil, fmt.Errorf("message photos: %w", err)
	}
	for _, p := range ps {
		out[p.ProviderMessageID] = append(out[p.ProviderMessageID], p)
	}
	return out, nil
}

// PhotosToDelete: rows due for deletion — asked for (account deletion), or
// never on a task and older than the retention. Oldest first.
func (s *PGStore) PhotosToDelete(ctx context.Context, olderThan time.Time, limit int) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+photoCols+` FROM channel_photos
		WHERE delete_requested_at IS NOT NULL OR (task_id IS NULL AND created_at < $1)
		ORDER BY created_at LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("photos to delete: %w", err)
	}
	return scanPhotos(rows)
}

// DeletePhotoRows removes rows whose objects are gone from the bucket.
func (s *PGStore) DeletePhotoRows(ctx context.Context, ids []string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM channel_photos WHERE id = ANY($1::uuid[])`, pgUUIDArray(ids)); err != nil {
		return fmt.Errorf("delete photo rows: %w", err)
	}
	return nil
}

// RequestPhotoDeletionTx marks every photo a user's numbers sent for
// deletion, inside the account deletion's transaction and BEFORE
// PurgeUserTx removes the identities (which then leaves identity_id NULL:
// nothing on the row names the person). The bucket objects and rows are
// deleted after the commit and by the sweeper, which retries. Returns the
// count. A database without the table is not an error.
func RequestPhotoDeletionTx(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('public.channel_photos') IS NOT NULL
		AND to_regclass('public.channel_identities') IS NOT NULL`).Scan(&present); err != nil {
		return 0, fmt.Errorf("request photo deletion: probe: %w", err)
	}
	if !present {
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE public.channel_photos
		   SET delete_requested_at = coalesce(delete_requested_at, now())
		 WHERE identity_id IN (SELECT id FROM public.channel_identities WHERE user_id = $1::uuid)`, userID)
	if err != nil {
		return 0, fmt.Errorf("request photo deletion: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("request photo deletion: count: %w", err)
	}
	return n, nil
}

// pgTextArray is a Postgres text[] literal, every element quoted.
func pgTextArray(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	return "{" + strings.Join(q, ",") + "}"
}

// withoutPhotoAsks drops the questions a photo answers when the draft has
// one.
func withoutPhotoAsks(asks []llm.PlaybookAsk, hasPhoto bool) []llm.PlaybookAsk {
	if !hasPhoto {
		return asks
	}
	var out []llm.PlaybookAsk
	for _, a := range asks {
		if !a.Photo {
			out = append(out, a)
		}
	}
	return out
}
