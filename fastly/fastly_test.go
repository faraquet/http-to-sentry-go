package fastly

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestChallengeHandler(t *testing.T) {
	serviceID := "service-123"
	sum := sha256.Sum256([]byte(serviceID))
	expected := hex.EncodeToString(sum[:]) + "\n"

	req := httptest.NewRequest(http.MethodGet, "/.well-known/fastly/logging/challenge", nil)
	rw := httptest.NewRecorder()

	ChallengeHandler(serviceID)(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	if rw.Body.String() != expected {
		t.Fatalf("unexpected body: %q", rw.Body.String())
	}
}

func TestHandleEventsAcceptsJSON(t *testing.T) {
	payload := `{"timestamp":"2026-01-29T11:41:12+0000","response_state":"ERROR","response_status":503,"response_reason":"origin timeout"}`

	var captured []*sentry.Event
	h := Handler{
		MaxBodyBytes: 1024,
		Capture: func(evt *sentry.Event) *sentry.EventID {
			captured = append(captured, evt)
			id := sentry.EventID("test")
			return &id
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/fastly", strings.NewReader(payload))
	w := httptest.NewRecorder()

	h.HandleEvents(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured))
	}
	if captured[0].Message == "" {
		t.Fatalf("expected message to be set")
	}
}
