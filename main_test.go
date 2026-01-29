package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireBearerWhenTokenSet(t *testing.T) {
	cfg := config{authToken: "secret"}
	req := httptest.NewRequest(http.MethodPost, "/ingest", nil)
	rw := httptest.NewRecorder()

	if requireBearer(rw, req, cfg) {
		t.Fatalf("expected bearer auth to be required")
	}
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}

	req.Header.Set("Authorization", "Bearer secret")
	rw = httptest.NewRecorder()
	if !requireBearer(rw, req, cfg) {
		t.Fatalf("expected bearer auth to pass")
	}
}

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()

	handleHealth(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	if strings.TrimSpace(rw.Body.String()) != "ok" {
		t.Fatalf("unexpected body: %q", rw.Body.String())
	}
}

func TestHandleIngestText(t *testing.T) {
	cfg := config{maxBodyBytes: 1024}
	body := strings.NewReader("hello")
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	rw := httptest.NewRecorder()

	handleIngest(rw, req, cfg)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rw.Code)
	}
}
