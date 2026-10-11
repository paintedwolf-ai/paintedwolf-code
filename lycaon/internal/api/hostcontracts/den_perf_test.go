package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHandleDenPerfEventsWritesWhenCaptureOn(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "den-perf.jsonl")
	t.Setenv("LYCAON_DEN_PERF_DEBUG", "1")
	t.Setenv("LYCAON_DEN_PERF_DEBUG_FILE", logPath)
	observability.CloseDenPerfDebug()

	srv := contractfixture.NewTestServer(t)
	body, err := json.Marshal(wire.DenPerfEventsRequest{
		Events: []wire.DenPerfEvent{{
			ObservedAt: "2026-07-15T12:00:00Z",
			Channel:    "perf",
			Event:      "loop-stall",
			Detail:     map[string]any{"lag_ms": 340, "recent": "thumbnail.capture:start@-40"},
		}},
	})
	testutil.FailErr(t, "marshal", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/debug/den-perf", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read capture", err)
	var captured observability.DenPerfDebugEntry
	testutil.FailErr(t, "decode captured event", json.Unmarshal(bytes.TrimSpace(data), &captured))
	if captured.Event != "loop-stall" || captured.Time.Format("2006-01-02T15:04:05Z07:00") != "2026-07-15T12:00:00Z" || captured.Detail["lag_ms"] != float64(340) {
		t.Fatalf("capture changed observation: %+v", captured)
	}
}

func TestHandleDenPerfEventsNoopsWhenCaptureOff(t *testing.T) {
	t.Setenv("LYCAON_DEN_PERF_DEBUG", "")
	t.Setenv("LYCAON_DEBUG_ALL", "")
	observability.CloseDenPerfDebug()

	srv := contractfixture.NewTestServer(t)
	body, err := json.Marshal(wire.DenPerfEventsRequest{
		Events: []wire.DenPerfEvent{{Event: "loop-stall", Detail: map[string]any{"lag_ms": 1}}},
	})
	testutil.FailErr(t, "marshal", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/debug/den-perf", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}
