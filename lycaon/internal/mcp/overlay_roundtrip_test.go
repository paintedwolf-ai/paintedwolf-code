package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newRoundTripRegistry(t *testing.T) (*mcp.Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	stageDistro(t, "providers: []\nprofiles: {}\n")
	globalPath := filepath.Join(dir, "mcp.yaml")

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          dir,
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	t.Cleanup(func() { _ = reg.Close() })
	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))
	return reg, globalPath
}

func createRemote(t *testing.T, reg *mcp.Runtime, id, url string) api.McpProvider {
	t.Helper()
	yes := true
	row, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source:  "custom",
		ID:      id,
		URL:     url,
		Enabled: &yes,
	}, "")
	testutil.FailErr(t, "create "+id, err)
	return row
}

// A transport switch persists the new field and an explicit clear of the other.
func TestUpdateProviderURLSurvivesRoundTrip(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	createRemote(t, reg, "probe", "https://a.example.com/mcp")

	next := "https://b.example.com/mcp"
	row, err := reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "probe",
		api.UpdateMcpProviderRequest{URL: &next}, "")
	testutil.FailErr(t, "update url", err)

	if row.Status == api.McpStatusRejected {
		t.Fatalf("url change rejected the row: %s", row.LastError)
	}
	if row.URL != next {
		t.Fatalf("url = %q want %q", row.URL, next)
	}
	if row.Command != "" {
		t.Fatalf("command = %q want cleared", row.Command)
	}

	testutil.FailErr(t, "reload", reg.Catalog.Load(context.Background()))
	reloaded, ok := reg.Catalog.GetProvider(context.Background(), mcp.CallScope{}, "probe")
	if !ok {
		t.Fatal("row missing after reload")
	}
	if reloaded.Status == api.McpStatusRejected || reloaded.URL != next {
		t.Fatalf("after reload: status=%s url=%q lastErr=%s", reloaded.Status, reloaded.URL, reloaded.LastError)
	}

	data, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read overlay", err)
	if !strings.Contains(string(data), next) {
		t.Fatalf("overlay did not persist the new url:\n%s", data)
	}
}

func TestUpdateProviderToolLoadingSurvivesRoundTrip(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	row := createRemote(t, reg, "probe", "https://a.example.com/mcp")
	if row.ToolLoading != api.McpToolLoadingAuto {
		t.Fatalf("default tool_loading = %q", row.ToolLoading)
	}

	mode := api.McpToolLoadingAlways
	row, err := reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "probe",
		api.UpdateMcpProviderRequest{ToolLoading: &mode}, "")
	testutil.FailErr(t, "update tool loading", err)
	if row.ToolLoading != api.McpToolLoadingAlways {
		t.Fatalf("tool_loading = %q", row.ToolLoading)
	}
	if modes := reg.Catalog.ToolLoadingModes(context.Background(), ""); !modes["probe"] {
		t.Fatalf("tool loading modes = %v", modes)
	}

	testutil.FailErr(t, "reload", reg.Catalog.Load(context.Background()))
	reloaded, ok := reg.Catalog.GetProvider(context.Background(), mcp.CallScope{}, "probe")
	if !ok || reloaded.ToolLoading != api.McpToolLoadingAlways {
		t.Fatalf("reloaded = %+v ok=%v", reloaded, ok)
	}
	data, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read overlay", err)
	if !strings.Contains(string(data), "tool_loading: always") {
		t.Fatalf("overlay missing tool_loading:\n%s", data)
	}
}

func TestUpdateProviderRejectsEmptyToolLoading(t *testing.T) {
	reg, _ := newRoundTripRegistry(t)
	createRemote(t, reg, "probe", "https://a.example.com/mcp")

	empty := api.McpToolLoading("")
	_, err := reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "probe",
		api.UpdateMcpProviderRequest{ToolLoading: &empty}, "")
	if err == nil {
		t.Fatal("empty tool_loading accepted")
	}
}

func TestUpdateProviderCommandSurvivesRoundTrip(t *testing.T) {
	reg, _ := newRoundTripRegistry(t)
	createRemote(t, reg, "probe", "https://a.example.com/mcp")

	cmd := "/usr/local/bin/some-mcp"
	row, err := reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "probe",
		api.UpdateMcpProviderRequest{Command: &cmd}, "")
	testutil.FailErr(t, "update command", err)
	if row.Status == api.McpStatusRejected {
		t.Fatalf("command change rejected the row: %s", row.LastError)
	}
	if row.Command != cmd || row.URL != "" {
		t.Fatalf("row = %+v want command-only", row)
	}
}

// Rejected overlay rows stay editable.
func TestRejectedRowRemainsEditable(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	testutil.FailErr(t, "seed rejected row", os.WriteFile(globalPath, []byte(`providers:
  - id: team
    url: http://intel.example/mcp
    enabled: true
`), 0o600))
	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))

	row, ok := reg.Catalog.GetProvider(context.Background(), mcp.CallScope{}, "team")
	if !ok || row.Status != api.McpStatusRejected {
		t.Fatalf("fixture must start rejected: %+v ok=%v", row, ok)
	}
	if row.LastError != mcp.RejectRemoteRequiresHTTPS {
		t.Fatalf("reject reason = %q", row.LastError)
	}

	httpsURL := "https://intel.example/mcp"
	fixed, err := reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "team",
		api.UpdateMcpProviderRequest{URL: &httpsURL}, "")
	testutil.FailErr(t, "correct rejected row", err)
	if fixed.Status == api.McpStatusRejected {
		t.Fatalf("row still rejected after correction: %s", fixed.LastError)
	}

	// Enable/disable must reach it too.
	testutil.FailErr(t, "toggle", reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "team", false, ""))
}

func TestCreateProviderPersistsTokenTokenWire(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	row, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source:         "custom",
		ID:             "pd-eu",
		URL:            "https://mcp.eu.pagerduty.com/mcp",
		Token:          "u+secret",
		CredentialWire: api.McpCredentialWireTokenToken,
	}, "")
	testutil.FailErr(t, "create", err)
	if row.CredentialWire != api.McpCredentialWireTokenToken {
		t.Fatalf("credential_wire = %q", row.CredentialWire)
	}
	data, err := os.ReadFile(globalPath)
	testutil.FailErr(t, "read overlay", err)
	if !strings.Contains(string(data), "token_token") {
		t.Fatalf("overlay missing credential_wire:\n%s", data)
	}
	testutil.FailErr(t, "reload", reg.Catalog.Load(context.Background()))
	reloaded, ok := reg.Catalog.GetProvider(context.Background(), mcp.CallScope{}, "pd-eu")
	if !ok || reloaded.CredentialWire != api.McpCredentialWireTokenToken {
		t.Fatalf("after reload: ok=%v wire=%q", ok, reloaded.CredentialWire)
	}
}

// Create over a rejected id is a collision.
func TestCreateOverRejectedIDCollides(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	testutil.FailErr(t, "seed rejected row", os.WriteFile(globalPath, []byte(`providers:
  - id: team
    url: http://intel.example/mcp
    enabled: true
`), 0o600))
	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))

	_, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "team",
		URL:    "http://127.0.0.1:8765/mcp",
	}, "")
	if !mcp.IsAdminCode(err, mcp.RejectDuplicateID) {
		t.Fatalf("create over a rejected id = %v want a collision", err)
	}
}

// Unreadable user overlay becomes a rejected row; Load still succeeds.
func TestUnreadableUserOverlayDegradesInsteadOfFailingLoad(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	testutil.FailErr(t, "write junk", os.WriteFile(globalPath, []byte("providers: [[[not yaml"), 0o600))

	if err := reg.Catalog.Load(context.Background()); err != nil {
		t.Fatalf("Load must not fail on an unreadable user overlay: %v", err)
	}
	rows := reg.Catalog.ListProviders(context.Background(), mcp.CallScope{})
	var found bool
	for _, row := range rows {
		if row.Status == api.McpStatusRejected && row.LastError == mcp.RejectUnreadableLayer {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unreadable_layer row, got %+v", rows)
	}
}

func TestServersKeyIsUnknownField(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	testutil.FailErr(t, "write unknown key", os.WriteFile(globalPath, []byte("servers: []\n"), 0o600))

	if err := reg.Catalog.Load(context.Background()); err != nil {
		t.Fatalf("Load must surface an invalid overlay as a rejected row: %v", err)
	}
	for _, row := range reg.Catalog.ListProviders(context.Background(), mcp.CallScope{}) {
		if row.Status == api.McpStatusRejected && row.LastError == mcp.RejectOverlayUnknownField {
			return
		}
	}
	t.Fatal("servers key did not surface overlay_unknown_field")
}

func TestDuplicateUserOverlayIDIsReported(t *testing.T) {
	reg, globalPath := newRoundTripRegistry(t)
	testutil.FailErr(t, "write dupes", os.WriteFile(globalPath, []byte(`providers:
  - id: dup
    url: http://127.0.0.1:1/mcp
  - id: dup
    url: http://127.0.0.1:2/mcp
`), 0o600))

	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))
	var found bool
	for _, row := range reg.Catalog.ListProviders(context.Background(), mcp.CallScope{}) {
		if row.Status == api.McpStatusRejected && row.LastError == mcp.RejectDuplicateID {
			found = true
		}
	}
	if !found {
		t.Fatal("duplicate id in the user overlay was not reported as duplicate_id")
	}
}
