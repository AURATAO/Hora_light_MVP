package main

import (
	"context"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"hora-auth/helpers"
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
	// No test reaches Google: createTask and updateTask geocode in the
	// background, and a developer's shell may have a real key set. Tests
	// that exercise geocoding install their own fake (task_geocode_test.go).
	geocodeFn = func(context.Context, string) (float64, float64, error) { return 0, 0, helpers.ErrGeocodeNoKey }
	os.Unsetenv("GOOGLE_MAPS_API_KEY") // so no background lookup is ever started
	os.Exit(m.Run())
}

// requireOrSkipDB is the one way a DB-backed test steps aside when
// TEST_DATABASE_URL is unset. By default it skips, as before — and `go test`
// prints `ok` for a package whose DB tests all skipped, which is how three
// "green" runs on 2026-10-27 proved nothing about the settlement lock. With
// REQUIRE_DB_TESTS=1 it FAILS instead, so a merge gate that forgot the
// database cannot pass quietly (runbook phase 1).
func requireOrSkipDB(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_TESTS") == "1" {
		t.Fatalf("%s (REQUIRE_DB_TESTS=1: a DB-backed test may not skip)", msg)
	}
	t.Skip(msg)
}
