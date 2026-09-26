package main

import "testing"

// The URL parser behind every signed photo link. Pure, and pinned because the
// stored form, the signed form and the fixture strings in the payments suites
// all have to round-trip through it without surprise.
func TestTaskPhotoObjectKey(t *testing.T) {
	const base = "https://akxsdkerudurzcemurrb.supabase.co/storage/v1/object"
	cases := []struct {
		name string
		in   string
		key  string
		ok   bool
	}{
		{"stored public form", base + "/public/task-completions/completions/abc/1.jpg", "completions/abc/1.jpg", true},
		{"signed form with token", base + "/sign/task-completions/completions/abc/1.jpg?token=eyJ.x.y", "completions/abc/1.jpg", true},
		{"bare object form", base + "/task-completions/completions/abc/1.jpg", "completions/abc/1.jpg", true},
		{"another bucket", base + "/public/avatars/u/1.png", "", false},
		{"a test fixture", "https://x/done.jpg", "", false},
		{"empty", "", "", false},
		{"path traversal", base + "/public/task-completions/../secrets", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := taskPhotoObjectKey(tc.in)
			if ok != tc.ok || key != tc.key {
				t.Errorf("taskPhotoObjectKey(%q) = (%q, %v), want (%q, %v)", tc.in, key, ok, tc.key, tc.ok)
			}
		})
	}
}

// What the database stores never carries a token, whatever the client sent.
func TestCanonicalTaskPhotoURLStripsTokens(t *testing.T) {
	t.Setenv("SUPABASE_PROJECT_URL", "https://proj.supabase.co")
	signed := "https://proj.supabase.co/storage/v1/object/sign/task-completions/completions/t/9.jpg?token=abc"
	want := "https://proj.supabase.co/storage/v1/object/public/task-completions/completions/t/9.jpg"
	if got := canonicalTaskPhotoURL(signed); got != want {
		t.Errorf("canonical = %q, want %q", got, want)
	}
	// Not ours: untouched, so a fixture URL in a test body stays what it was.
	if got := canonicalTaskPhotoURL("https://x/done.jpg"); got != "https://x/done.jpg" {
		t.Errorf("foreign URL rewritten to %q", got)
	}
}

// Without storage configured — every test run — signing is a pass-through
// rather than an error, which is what keeps the payments suites' fixture
// URLs asserting cleanly.
func TestSignTaskPhotoURLPassesThroughWithoutStorage(t *testing.T) {
	if taskPhotoStorage() != nil {
		t.Skip("storage configured in this environment")
	}
	in := "https://proj.supabase.co/storage/v1/object/public/task-completions/completions/t/9.jpg"
	if got := signTaskPhotoURL(in, taskPhotoLinkTTL); got != in {
		t.Errorf("signed without a client: %q", got)
	}
}
