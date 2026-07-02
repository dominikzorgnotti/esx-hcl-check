package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckEndpoints(t *testing.T) {
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer reachable.Close()

	// Start then immediately stop a server to get a URL that refuses connections.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	client := &http.Client{Timeout: 2 * time.Second}

	if f := checkEndpoints(client, []hclEndpoint{{"ok", reachable.URL}}); len(f) != 0 {
		t.Errorf("reachable endpoint reported failures: %v", f)
	}

	if f := checkEndpoints(client, []hclEndpoint{{"down", downURL}}); len(f) != 1 {
		t.Errorf("unreachable endpoint: got %d failures, want 1 (%v)", len(f), f)
	}

	// Mixed: only the down one should be reported.
	f := checkEndpoints(client, []hclEndpoint{{"ok", reachable.URL}, {"down", downURL}})
	if len(f) != 1 {
		t.Errorf("mixed: got %d failures, want 1 (%v)", len(f), f)
	}
}
