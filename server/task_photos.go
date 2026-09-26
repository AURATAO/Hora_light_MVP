package main

// Task photos: the supporter's proof-of-work photo and the receipt photo.
//
// Both are uploaded through POST /tasks/:id/completion-photo into the
// `task-completions` bucket and stored on the task as URLs. Until 2026-09-26
// that bucket was PUBLIC and the stored URL was the public one, so anybody
// holding the link — or guessing a task id and a timestamp — could read a
// receipt with a stranger's purchases on it and a photo of the inside of
// their home. The rest of the task payload is behind the requester/assignee
// check in getTask and getWorklogs; the photos were not.
//
// THE RULE NOW: the bucket is private, the database keeps a CANONICAL
// reference (the same public-form URL it always did, which is stable and
// parses back to an object key), and every read that hands a photo to a
// client SIGNS it here first. A signed URL is short-lived and only ever
// issued from inside a handler that has already decided the caller may see
// the task, so the photo inherits exactly the authorization the payload has.
//
// Nothing outside this file knows the bucket name or the URL shape.

import (
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	storage_go "github.com/supabase-community/storage-go"
)

// taskPhotoBucket is the one bucket task photos live in. Private since
// 20260926_task_completions_bucket_private; the avatars bucket is a different
// bucket with a different rule (CLAUDE.md Rule 2) and is not touched here.
const taskPhotoBucket = "task-completions"

// How long a signed photo link stays valid.
//
//	taskPhotoLinkTTL   a task screen. Long enough to survive a slow scroll and
//	                   a pull-to-refresh, short enough that a forwarded link
//	                   is dead by the time it is read.
//	taskPhotoEmailTTL  the completion email, which a requester may open days
//	                   later. An email is already a copy of the photo in
//	                   somebody's inbox; the link inside it is bounded anyway.
const (
	taskPhotoLinkTTL  = time.Hour
	taskPhotoEmailTTL = 7 * 24 * time.Hour
)

// taskPhotoObjectKey pulls the object key out of any URL shape this codebase
// has ever produced for a task photo:
//
//	https://<project>/storage/v1/object/public/task-completions/<key>   (stored form)
//	https://<project>/storage/v1/object/sign/task-completions/<key>?token=…
//	https://<project>/storage/v1/object/task-completions/<key>
//
// ok is false for anything else — a photo from another bucket, a test
// fixture's https://x/done.jpg, an empty string — and callers pass those
// through untouched.
func taskPhotoObjectKey(raw string) (key string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	path := u.Path
	marker := "/storage/v1/object/"
	i := strings.Index(path, marker)
	if i < 0 {
		return "", false
	}
	rest := path[i+len(marker):]
	for _, prefix := range []string{"public/", "sign/", "authenticated/"} {
		rest = strings.TrimPrefix(rest, prefix)
	}
	if !strings.HasPrefix(rest, taskPhotoBucket+"/") {
		return "", false
	}
	key = strings.TrimPrefix(rest, taskPhotoBucket+"/")
	if key == "" || strings.Contains(key, "..") {
		return "", false
	}
	return key, true
}

// canonicalTaskPhotoURL is what the database stores: the stable public-form
// URL for the object, with no token. A client that echoes back a signed URL
// from an upload response (or from a task it just read) is normalised here so
// that a token never lands in a column.
func canonicalTaskPhotoURL(raw string) string {
	key, ok := taskPhotoObjectKey(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	base := strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/")
	if base == "" {
		// No project URL to rebuild against: keep the caller's, minus any
		// query string, which is where a token would be.
		if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
			u.RawQuery, u.Fragment = "", ""
			return u.String()
		}
		return strings.TrimSpace(raw)
	}
	return base + "/storage/v1/object/public/" + taskPhotoBucket + "/" + key
}

// signTaskPhotoURL turns a stored task photo reference into a link a client
// can actually load, valid for ttl.
//
// Returns the input unchanged when it is not one of ours (see
// taskPhotoObjectKey), when storage is not configured (tests, a bare local
// run), or when Stripe-style "the network had a bad afternoon" happens at
// the signing call — a task screen with a broken image is better than a
// task screen that 500s, and the failure is logged either way.
func signTaskPhotoURL(raw string, ttl time.Duration) string {
	key, ok := taskPhotoObjectKey(raw)
	if !ok {
		return raw
	}
	st := taskPhotoStorage()
	if st == nil {
		return raw
	}
	seconds := int(ttl / time.Second)
	if seconds <= 0 {
		seconds = int(taskPhotoLinkTTL / time.Second)
	}
	signed, err := st.CreateSignedUrl(taskPhotoBucket, key, seconds)
	if err != nil || signed.SignedURL == "" {
		log.Printf("[task-photos][ERROR] could not sign %s/%s: %v", taskPhotoBucket, key, err)
		return raw
	}
	return signed.SignedURL
}

var (
	taskPhotoStorageOnce   sync.Once
	taskPhotoStorageClient *storage_go.Client
)

// taskPhotoStorage is the storage client used for signing, built once from
// the same two env vars the upload path uses. Nil when either is unset, which
// is every test run: signTaskPhotoURL then passes URLs through untouched and
// the tests keep asserting on the fixture strings they wrote.
func taskPhotoStorage() *storage_go.Client {
	taskPhotoStorageOnce.Do(func() {
		base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SUPABASE_PROJECT_URL")), "/")
		key := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
		if base == "" || key == "" {
			return
		}
		// Same header pair as newStorageClientV1: Kong wants `apikey` as well
		// as the bearer, and a new-style sb_secret key is only resolved via
		// `apikey`.
		taskPhotoStorageClient = storage_go.NewClient(base+"/storage/v1", key, map[string]string{"apikey": key})
	})
	return taskPhotoStorageClient
}
