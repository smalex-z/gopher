package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

func initLogsTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	if err := db.Initialize(dsn); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
}

func TestWebSocketDuringSetup_RejectsAfterLocalSetupDone(t *testing.T) {
	initLogsTestDB(t)
	settings, err := db.GetSettings()
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}
	settings.LocalSetupDone = true
	settings.FirewallMode = "gopher"
	if err := db.SaveSettings(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	h := NewLogsHandler(service.NewLogHub())
	r := httptest.NewRequest(http.MethodGet, "/api/local/logs/ws", nil)
	w := httptest.NewRecorder()

	h.WebSocketDuringSetup(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when local setup already done, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestWebSocketDuringSetup_AllowsBeforeLocalSetupDone(t *testing.T) {
	initLogsTestDB(t)
	settings, err := db.GetSettings()
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}
	settings.LocalSetupDone = false
	if err := db.SaveSettings(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	h := NewLogsHandler(service.NewLogHub())
	r := httptest.NewRequest(http.MethodGet, "/api/local/logs/ws", nil)
	w := httptest.NewRecorder()

	h.WebSocketDuringSetup(w, r)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected non-401 before local setup is done, got %d body=%s", w.Code, w.Body.String())
	}
}
