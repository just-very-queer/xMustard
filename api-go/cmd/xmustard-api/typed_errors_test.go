package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// respondError maps an error to an HTTP status by TYPE, not by message wording — so
// re-phrasing an internal error can no longer flip the status (XM-PRO-013).
func TestRespondErrorMapsByTypeNotWording(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", workspaceops.Invalid("runtime x is not available"), 400},
		{"invalid reworded -> same status", workspaceops.Invalid("totally different wording entirely"), 400},
		{"not found", workspaceops.NotFoundErr("no such run"), 404},
		{"conflict", workspaceops.Conflict("plan already approved"), 409},
		{"unavailable", workspaceops.Unavailable("postgres not configured"), 503},
		{"os not exist", os.ErrNotExist, 404},
		{"raw internal", errors.New("some unexpected failure"), 500},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		respondError(rec, c.err)
		if rec.Code != c.status {
			t.Errorf("%s: got status %d, want %d", c.name, rec.Code, c.status)
		}
	}

	// A wrapped Invalid is still classified as invalid input (errors.As chain).
	if !workspaceops.IsInvalidInput(workspaceops.Invalid("bad")) {
		t.Error("a typed Invalid DomainError must satisfy IsInvalidInput")
	}

	// The internal class must not disclose its wrapped cause (e.g. a host path).
	rec := httptest.NewRecorder()
	internal := (&workspaceops.DomainError{Class: workspaceops.ClassInternal, Public: "ignored"}).
		WithCause(errors.New("/secret/host/path/leaked"))
	respondError(rec, internal)
	if rec.Code != 500 {
		t.Fatalf("internal class: got %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "host") {
		t.Fatalf("internal error leaked its cause to the client: %s", rec.Body.String())
	}
}
