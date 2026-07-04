package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	// Status code must be 200 OK.
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.StatusCode)
	}

	// Content-Type must be JSON.
	if ct := res.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("unexpected Content-Type: %q", ct)
	}

	// Body must decode into healthResponse with expected fields.
	var body healthResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("expected status \"ok\", got %q", body.Status)
	}
	if body.Service != "almanac" {
		t.Errorf("expected service \"almanac\", got %q", body.Service)
	}
	if body.Time == "" {
		t.Error("expected non-empty time field")
	}
}
