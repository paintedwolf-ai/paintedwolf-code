package security

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMCPEnableThenCheck(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: &enabled})
	testutil.FailErr(t, "marshal request", err)
	req := authedRequest(t, http.MethodPatch, "/v1/mcp/providers/svca", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodPost, "/v1/mcp/providers/check", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("check status = %d", w.Code)
	}
	var check wire.McpCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &check); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	for _, row := range check.Providers {
		if row.ProviderID == "svca" {
			if row.Status != wire.McpCheckRowStatusHealthy {
				t.Fatalf("check not healthy: status=%s code=%s", row.Status, row.Code)
			}
		}
	}
}

func TestMCPDisabledToolsUnavailable(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	reg := h.MCPRegistry()
	if reg == nil {
		t.Fatal("MCP registry required")
	}
	names := reg.RegisteredMCPTools()
	for _, name := range names {
		t.Fatalf("unexpected registered mcp tool %q while disabled", name)
	}
	req := authedRequest(t, http.MethodGet, "/v1/mcp/providers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var listed wire.McpProviderListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	for _, s := range listed.Providers {
		if s.Enabled {
			t.Fatalf("provider %q enabled by default", s.ID)
		}
	}
}
