package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestFocusEndpointInvokesHandler verifies POST /api/focus calls the registered
// focus handler. This is the wiring a second launch relies on to raise the
// running window (instead of the new process dying silently — the "闪退" bug).
func TestFocusEndpointInvokesHandler(t *testing.T) {
	s := newTestServer(t)
	called := 0
	s.SetFocusHandler(func() { called++ })

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/focus", s.token, nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if called != 1 {
		t.Fatalf("focus handler called %d times, want 1", called)
	}
	var body struct{ Focused bool }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if !body.Focused {
		t.Fatalf("focused = false, want true when a handler is registered")
	}
}

// TestFocusEndpointNoHandlerStillOK verifies the endpoint is a safe no-op (200)
// when no handler is registered — e.g. the headless daemon has no window to raise.
func TestFocusEndpointNoHandlerStillOK(t *testing.T) {
	s := newTestServer(t) // no SetFocusHandler
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/focus", s.token, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (no-op)", w.Code)
	}
	var body struct{ Focused bool }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Focused {
		t.Fatalf("focused = true, want false when no handler is registered")
	}
}
