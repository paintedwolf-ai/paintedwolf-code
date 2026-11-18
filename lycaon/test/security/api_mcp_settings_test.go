package security

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	testutil.FailErr(t, "decode provider check", json.Unmarshal(w.Body.Bytes(), &check))
	index := slices.IndexFunc(check.Providers, func(row wire.McpCheckRow) bool {
		return row.ProviderID == "svca"
	})
	if index < 0 {
		t.Fatal("check omitted the installed provider")
	}
	if row := check.Providers[index]; row.Status != wire.McpCheckRowStatusHealthy {
		t.Fatalf("check not healthy: status=%s code=%s", row.Status, row.Code)
	}

	const toolName = "mcp_svca_do"
	req = authedRequest(t, http.MethodGet, "/v1/mcp/providers/svca/tools", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list tools status = %d body=%s", w.Code, w.Body.String())
	}
	var listed wire.McpToolListResponse
	testutil.FailErr(t, "decode provider tools", json.Unmarshal(w.Body.Bytes(), &listed))
	if len(listed.Tools) != 1 || listed.Tools[0].Name != toolName {
		t.Fatalf("listed tools = %+v, want %s", listed.Tools, toolName)
	}
	if _, ok := h.ToolRegistry.Meta(toolName); !ok {
		t.Fatalf("enabled tool %q is absent from dispatch", toolName)
	}
}

func TestMCPDisabledToolsUnavailable(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	for _, meta := range h.ToolRegistry.List() {
		if meta.IsMCP() {
			t.Fatalf("disabled provider tool %q is present in dispatch", meta.Name)
		}
	}
	req := authedRequest(t, http.MethodGet, "/v1/mcp/providers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var listed wire.McpProviderListResponse
	testutil.FailErr(t, "decode provider list", json.Unmarshal(w.Body.Bytes(), &listed))
	index := slices.IndexFunc(listed.Providers, func(provider wire.McpProvider) bool {
		return provider.ID == "svca"
	})
	if index < 0 {
		t.Fatal("list omitted the installed provider")
	}
	for _, provider := range listed.Providers {
		if provider.Enabled {
			t.Fatalf("provider %q is enabled by default", provider.ID)
		}
	}
}
