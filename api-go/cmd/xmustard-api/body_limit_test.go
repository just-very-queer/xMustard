package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request body past maxRequestBodyBytes must be cut off (so a huge POST can't
// OOM the server), while a normal body passes through untouched.
func TestBodyLimitMiddlewareCapsOversizedBody(t *testing.T) {
	var readErr error
	handlerRan := false
	h := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		if _, readErr = io.ReadAll(r.Body); readErr != nil {
			http.Error(w, "too big", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// oversized: one byte past the cap is refused (413) before any handler buffers it —
	// both with a declared length and with an unknown (chunked) length.
	for _, declared := range []bool{true, false} {
		handlerRan = false
		over := httptest.NewRequest("POST", "/api/x", strings.NewReader(strings.Repeat("A", maxRequestBodyBytes+1)))
		if !declared {
			over.ContentLength = -1
		}
		recOver := httptest.NewRecorder()
		h.ServeHTTP(recOver, over)
		if handlerRan || recOver.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized body (declared=%v): want 413 before the handler, got %d handlerRan=%v", declared, recOver.Code, handlerRan)
		}
	}

	// normal body passes
	readErr = nil
	ok := httptest.NewRequest("POST", "/api/x", strings.NewReader(`{"content":"ok"}`))
	recOK := httptest.NewRecorder()
	h.ServeHTTP(recOK, ok)
	if readErr != nil || recOK.Code != http.StatusOK {
		t.Fatalf("normal body should pass: err=%v code=%d", readErr, recOK.Code)
	}
}

// Fable runtime review #3: a declared body over the per-request cap can never be
// admitted, so it must be 413 (permanent), not 503 + Retry-After (retry forever).
func TestOversizedDeclaredBodyIs413NotRetryable(t *testing.T) {
	h := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler must not run for an oversized declared body")
	}))
	req := httptest.NewRequest("POST", "/api/workspaces/ws/context", strings.NewReader(`{"content":"x"}`))
	req.ContentLength = 99999999999
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("want 413 without Retry-After, got %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
