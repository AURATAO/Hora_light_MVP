package main

// Inbound chat photos, server side (internal/channel/photos.go has the
// rules). This file is the private bucket, the TalkJS file upload, the
// signed links handed to the task screen and Ops Chats, and the sweeper that
// deletes photos.
//
// Bucket: channel-photos (migration 20261110150000_channel_photos.sql),
// PRIVATE. Written and read with the service key; never a public URL. A
// signed link is issued only inside a handler that has already decided the
// caller may see the photo, and lives channel.PhotoLinkTTL.
//
// Switched on by CHANNEL_PHOTOS_ENABLED=1, after the migration is applied.
// Off: photos are answered "I can only read text", as before, and no read
// touches channel_photos.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	"hora-auth/internal/channel"
)

const channelPhotoBucket = "channel-photos"

// channelPhotosEnabled: CHANNEL_PHOTOS_ENABLED=1.
func channelPhotosEnabled() bool { return os.Getenv("CHANNEL_PHOTOS_ENABLED") == "1" }

// photoBlobs is the private bucket over Supabase Storage's REST API.
type photoBlobs struct {
	base string // https://<project>.supabase.co (no trailing slash)
	key  string // service role key
	http *http.Client
}

// photoBlobsFromEnv: nil when SUPABASE_PROJECT_URL or the service key is
// missing.
func photoBlobsFromEnv() *photoBlobs {
	base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SUPABASE_PROJECT_URL")), "/")
	key := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
	if base == "" || key == "" {
		return nil
	}
	return &photoBlobs{base: base, key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (b *photoBlobs) objectURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return b.base + "/storage/v1/object/" + channelPhotoBucket + "/" + strings.Join(parts, "/")
}

func (b *photoBlobs) do(ctx context.Context, method, u, contentType string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, fmt.Errorf("storage request: %w", scrubStorageErr(err))
	}
	// Kong wants `apikey` as well as the bearer (task_photos.go).
	req.Header.Set("Authorization", "Bearer "+b.key)
	req.Header.Set("apikey", b.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("storage: %w", scrubStorageErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, channel.MaxPhotoBytes+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("storage read: %w", scrubStorageErr(err))
	}
	return resp.StatusCode, raw, nil
}

// scrubStorageErr drops the URL net/http puts in its errors.
func scrubStorageErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

func (b *photoBlobs) PutPhoto(ctx context.Context, key, contentType string, data []byte) error {
	status, _, err := b.do(ctx, http.MethodPost, b.objectURL(key), contentType, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		// Status only: the body may echo the key.
		return fmt.Errorf("storage upload returned %d", status)
	}
	return nil
}

func (b *photoBlobs) GetPhoto(ctx context.Context, key string) ([]byte, error) {
	status, raw, err := b.do(ctx, http.MethodGet, b.objectURL(key), "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("storage download returned %d", status)
	}
	return raw, nil
}

// DeletePhotos removes objects; a key already gone is not an error.
func (b *photoBlobs) DeletePhotos(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string][]string{"prefixes": keys})
	if err != nil {
		return fmt.Errorf("storage delete: %w", err)
	}
	status, _, err := b.do(ctx, http.MethodDelete, b.base+"/storage/v1/object/"+channelPhotoBucket, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("storage delete returned %d", status)
	}
	return nil
}

// SignPhoto is a link to one object, valid for ttl.
func (b *photoBlobs) SignPhoto(ctx context.Context, key string, ttl time.Duration) (string, error) {
	body, err := json.Marshal(map[string]int{"expiresIn": int(ttl / time.Second)})
	if err != nil {
		return "", fmt.Errorf("storage sign: %w", err)
	}
	u := strings.Replace(b.objectURL(key), "/storage/v1/object/", "/storage/v1/object/sign/", 1)
	status, raw, err := b.do(ctx, http.MethodPost, u, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("storage sign returned %d", status)
	}
	var r struct {
		SignedURL string `json:"signedURL"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.SignedURL == "" {
		return "", fmt.Errorf("storage sign: no link in the answer")
	}
	if strings.HasPrefix(r.SignedURL, "http") {
		return r.SignedURL, nil
	}
	return b.base + "/storage/v1" + "/" + strings.TrimPrefix(r.SignedURL, "/"), nil
}

// photoLink is one photo as a client sees it.
type photoLink struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

// photoSigner is what reads need from the bucket (tests fake it).
type photoSigner interface {
	SignPhoto(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// signPhotos signs each photo; one that cannot be signed is left out and
// logged by id (never a URL).
func signPhotos(ctx context.Context, s photoSigner, ps []channel.Photo) []photoLink {
	out := []photoLink{}
	for _, p := range ps {
		u, err := s.SignPhoto(ctx, p.ObjectKey, channel.PhotoLinkTTL)
		if err != nil {
			log.Printf("[channel-photos][ERROR] sign photo=%s: %v", p.ID, err)
			continue
		}
		out = append(out, photoLink{URL: u, ContentType: p.ContentType})
	}
	return out
}

// Set at startup (RegisterChannelWebhooks) when photos are on; nil = off.
// Read by GET /tasks/:id.
var (
	chatPhotoStore  *channel.PGStore
	chatPhotoSigner photoSigner
)

// requestPhotosForTask: the photos the requester texted for this task, as
// signed links. Called only after getTask decided the caller is the
// requester, the assignee or ops.
func requestPhotosForTask(ctx context.Context, taskID string) []photoLink {
	if chatPhotoStore == nil || chatPhotoSigner == nil {
		return nil
	}
	ps, err := chatPhotoStore.TaskPhotos(ctx, taskID)
	if err != nil {
		log.Printf("[channel-photos] task=%s: %v", taskID, err)
		return nil
	}
	if len(ps) == 0 {
		return nil
	}
	return signPhotos(ctx, chatPhotoSigner, ps)
}

// ── TalkJS ─────────────────────────────────────────────────────────────────

// PostPhotoAsRequester uploads the photo to TalkJS and posts it into the
// task's chat as the requester (a file message). Same checks as words:
// the task is the sender's, open, with a supporter, and the pair not
// blocked.
func (t *talkjsRelay) PostPhotoAsRequester(ctx context.Context, taskID, userID string, data []byte, contentType, key string) error {
	pair, err := t.openRelay(ctx, taskID, userID)
	if err != nil {
		return fmt.Errorf("post photo as requester: %w", err)
	}
	token, err := talkjsUploadFile(ctx, data, contentType)
	if err != nil {
		return fmt.Errorf("post photo as requester: %w", err)
	}
	msg := map[string]any{
		"content": []map[string]string{{"type": "file", "fileToken": token}},
		"sender":  pair.requesterEmail,
		"type":    "UserMessage",
	}
	if err := t.sendAsRequester(ctx, taskID, msg, key); err != nil {
		return fmt.Errorf("post photo as requester: %w", err)
	}
	return nil
}

var photoExt = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp", "image/heic": "heic", "image/heif": "heif"}

// talkjsUploadFile: POST /files (multipart: file, filename, subtype=image)
// → fileToken.
func talkjsUploadFile(ctx context.Context, data []byte, contentType string) (string, error) {
	appID, secret, ok := talkjsConfigured()
	if !ok {
		return "", fmt.Errorf("talkjs not configured")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	name := "photo." + firstNonEmptyStr(photoExt[contentType], "jpg")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("talkjs upload: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("talkjs upload: %w", err)
	}
	for k, v := range map[string]string{"filename": name, "subtype": "image"} {
		if err := w.WriteField(k, v); err != nil {
			return "", fmt.Errorf("talkjs upload: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("talkjs upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, talkjsAPIBase+"/"+appID+"/files", &buf)
	if err != nil {
		return "", fmt.Errorf("talkjs upload: %w", scrubURLErr(err))
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("talkjs upload: %w", scrubURLErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("talkjs upload returned %d", resp.StatusCode)
	}
	var r struct {
		FileToken string `json:"fileToken"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.FileToken == "" {
		return "", fmt.Errorf("talkjs upload: no fileToken in the answer")
	}
	return r.FileToken, nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ── Deletion ───────────────────────────────────────────────────────────────

// photoDeleter is what the sweep needs from the bucket.
type photoDeleter interface {
	DeletePhotos(ctx context.Context, keys []string) error
}

// sweepPhotos deletes the photos that are due (channel.PGStore.PhotosToDelete:
// asked for by account deletion, or never on a task and older than
// channel.UnattachedPhotoRetention): the objects first, then the rows. A
// bucket failure leaves the rows, so the next sweep tries again — a row is
// never dropped while its object may still exist. Returns how many went.
func sweepPhotos(ctx context.Context, store *channel.PGStore, blobs photoDeleter) (int, error) {
	if store == nil || blobs == nil {
		return 0, nil
	}
	total := 0
	for round := 0; round < 10; round++ {
		ps, err := store.PhotosToDelete(ctx, time.Now().Add(-channel.UnattachedPhotoRetention), 100)
		if err != nil {
			return total, err
		}
		if len(ps) == 0 {
			return total, nil
		}
		keys := make([]string, 0, len(ps))
		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			keys, ids = append(keys, p.ObjectKey), append(ids, p.ID)
		}
		if err := blobs.DeletePhotos(ctx, keys); err != nil {
			return total, fmt.Errorf("sweep photos: %w", err)
		}
		if err := store.DeletePhotoRows(ctx, ids); err != nil {
			return total, fmt.Errorf("sweep photos: %w", err)
		}
		total += len(ps)
	}
	return total, nil
}

// photoSweep is set at startup when photos are on; account deletion runs
// it right after its commit (best effort; the watcher retries).
var photoSweep func(ctx context.Context) (int, error)

func runPhotoSweep(ctx context.Context, why string) {
	if photoSweep == nil {
		return
	}
	n, err := photoSweep(ctx)
	if err != nil {
		log.Printf("[channel-photos][ERROR] sweep (%s): %d deleted, then: %v", why, n, err)
		return
	}
	if n > 0 {
		log.Printf("[channel-photos] sweep (%s): %d deleted", why, n)
	}
}

// attachThreadPhotos puts signed links on the inbound messages that came
// with photos (Ops Chats). Ops-only route; nothing when photos are off.
func attachThreadPhotos(ctx context.Context, store *channel.PGStore, ch channel.Channel, msgs []channel.ThreadMessage) {
	if chatPhotoSigner == nil || store == nil {
		return
	}
	var ids []string
	for _, m := range msgs {
		if m.Direction == "inbound" && m.ProviderID != "" {
			ids = append(ids, m.ProviderID)
		}
	}
	byMsg, err := store.MessagePhotos(ctx, ch, ids)
	if err != nil {
		log.Printf("[channel-photos] thread photos: %v", err)
		return
	}
	for i := range msgs {
		if ps := byMsg[msgs[i].ProviderID]; len(ps) > 0 && msgs[i].Direction == "inbound" {
			for _, l := range signPhotos(ctx, chatPhotoSigner, ps) {
				msgs[i].Photos = append(msgs[i].Photos, l.URL)
			}
		}
	}
}
