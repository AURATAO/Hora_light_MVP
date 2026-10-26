package main

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// gin.SetMode writes a package-level variable, and it was being called from
// inside tests — including from helpers that several goroutines run at once
// (postGpsPing in the concurrency tests). Under -race that is a data race on
// gin's own state, and it failed on base as well as on this branch: a
// test-harness defect, never a product one, and the reason `go test -race`
// was red for weeks (PLAN §11).
//
// Set ONCE here, before any test in the package runs, and removed from all 36
// call sites plus one init(): leaving them in place would have left the racy
// pattern to be copied into the next test.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
