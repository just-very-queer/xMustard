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
	h := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, readErr = io.ReadAll(r.Body); readErr != nil {
			http.Error(w, "too big", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// oversized: one byte past the cap -> read fails -> 400, no unbounded buffering
	over := httptest.NewRequest("POST", "/api/x", strings.NewReader(strings.Repeat("A", maxRequestBodyBytes+1)))
	recOver := httptest.NewRecorder()
	h.ServeHTTP(recOver, over)
	if readErr == nil {
		t.Fatal("expected the body read to fail past maxRequestBodyBytes")
	}
	if recOver.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: expected 400, got %d", recOver.Code)
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
