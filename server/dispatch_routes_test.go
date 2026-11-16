package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
)

// The app calls /me/dispatch and /me/availability. The server had them only
// under /profile, every handler test called the handler directly, and so
// nothing noticed that the app's calls answered 404 (TestFlight build 19,
// 2026-11-16). This pins the paths the app actually uses to routes that
// exist.
func TestDispatchRoutesAreWhereTheAppCallsThem(t *testing.T) {
	r := gin.New()
	registerDispatchRoutes(r.Group("/me"))
	have := map[string]bool{}
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	for _, want := range []string{"GET /me/dispatch", "PUT /me/dispatch", "POST /me/availability"} {
		if !have[want] {
			t.Errorf("route %s is not registered", want)
		}
	}

	// And the app's own file still says /me/…: if it moves, this test says
	// which side to change.
	src, err := os.ReadFile("../mobile/src/lib/api.ts")
	if err != nil {
		t.Skipf("mobile sources not present: %v", err)
	}
	for _, path := range []string{`"/me/dispatch"`, `"/me/availability"`} {
		if !regexp.MustCompile(regexp.QuoteMeta(path)).Match(src) {
			t.Errorf("mobile/src/lib/api.ts no longer calls %s; update registerDispatchRoutes' mount in main.go to match", path)
		}
	}

	// main.go mounts it under /me.
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`meDispatchAPI := r\.Group\("/me"\)`).Match(main) || !regexp.MustCompile(`registerDispatchRoutes\(meDispatchAPI\)`).Match(main) {
		t.Error("main.go does not mount registerDispatchRoutes under /me")
	}
}
