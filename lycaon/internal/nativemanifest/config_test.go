package nativemanifest_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledApprovalReversibilityExhaustive(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateApprovalReversibility(); err != nil {
		t.Fatalf("ValidateApprovalReversibility: %v", err)
	}
	rev, rec, irr := cfg.ToolsByTier()
	if len(rev) == 0 || len(rec) == 0 {
		t.Fatalf("expected non-empty reversible/recoverable sets; got rev=%d rec=%d irr=%d", len(rev), len(rec), len(irr))
	}
	if len(irr) != 1 || irr[0] != "secret_revoke" {
		t.Fatalf("irreversible tools = %v, want [secret_revoke]", irr)
	}
}

func TestBundledToolContractsAreClosed(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateToolContracts(); err != nil {
		t.Fatalf("ValidateToolContracts: %v", err)
	}
	if cfg.TurnOrder["summarize"] != nativemanifest.TurnOrderLate ||
		cfg.TurnOrder["wait"] != nativemanifest.TurnOrderTerminal {
		t.Fatalf("turn_order = %+v", cfg.TurnOrder)
	}
	for _, tool := range []string{"fetch_url", "web_search"} {
		if cfg.BatchPolicy[tool] != nativemanifest.BatchSameTool || cfg.BatchOptInArg[tool] != "parallel" || cfg.BatchConcurrencyLimit[tool] != 4 {
			t.Fatalf("%s batch contract = policy %q opt-in %q cap %d", tool, cfg.BatchPolicy[tool], cfg.BatchOptInArg[tool], cfg.BatchConcurrencyLimit[tool])
		}
	}
	if got := cfg.BatchSerialWhenArg["fetch_url"]; got != "dest" {
		t.Fatalf("fetch_url batch_serial_when_arg = %q want dest", got)
	}
	if cfg.BatchConcurrencyLimit["summarize"] != 4 {
		t.Fatalf("summarize batch cap = %d want 4", cfg.BatchConcurrencyLimit["summarize"])
	}
}

func TestValidateToolContractsRejectsIncompleteOptInBatch(t *testing.T) {
	cfg := nativemanifest.Config{
		ApprovalReversibility: map[string]string{"fetch_url": nativemanifest.TierRecoverable},
		Owner:                 map[string]string{"fetch_url": "web_research"},
		BatchPolicy:           map[string]string{"fetch_url": nativemanifest.BatchSameTool},
		BatchOptInArg:         map[string]string{"fetch_url": "parallel"},
		Lifecycle:             map[string]string{"fetch_url": nativemanifest.LifecycleEffectAttempt},
		Families:              map[string][]string{"web": {"fetch_url"}},
	}
	if err := cfg.ValidateToolContracts(); err == nil || !strings.Contains(err.Error(), "missing batch_concurrency_limit") {
		t.Fatalf("ValidateToolContracts error = %v", err)
	}
}

func TestBundledGrantTargetScopesValid(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)

	known := func(tool string) bool {
		if cfg.HasTool(tool) {
			return true
		}
		_, ok := cfg.ApprovalReversibility[tool]
		return ok
	}
	if err := cfg.ValidateGrantTargetScopes(known); err != nil {
		t.Fatalf("ValidateGrantTargetScopes: %v", err)
	}
	if got := cfg.PathScopedTools(); len(got) == 0 {
		t.Fatal("expected at least one path-scoped tool")
	}
	if got := cfg.CommandScopedTools(); len(got) == 0 {
		t.Fatal("expected at least one command-scoped tool")
	}
}

func TestValidateGrantTargetScopesRejectsUnknownValue(t *testing.T) {
	cfg := nativemanifest.Config{
		GrantTargetScope: map[string]string{"write": "everywhere"},
	}
	err := cfg.ValidateGrantTargetScopes(func(string) bool { return true })
	if err == nil {
		t.Fatal("expected an unknown grant_target_scope value to be rejected")
	}
}

func TestValidateGrantTargetScopesRejectsUnknownTool(t *testing.T) {
	cfg := nativemanifest.Config{
		GrantTargetScope: map[string]string{"wrote": "path"},
	}
	err := cfg.ValidateGrantTargetScopes(func(string) bool { return false })
	if err == nil {
		t.Fatal("expected a grant_target_scope key naming no known tool to be rejected")
	}
}

func TestBundledProcessSpawningToolsValid(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	known := func(tool string) bool {
		if cfg.HasTool(tool) {
			return true
		}
		_, ok := cfg.ApprovalReversibility[tool]
		return ok
	}
	if err := cfg.ValidateSpawnsProcess(known); err != nil {
		t.Fatalf("ValidateSpawnsProcess: %v", err)
	}
	got := cfg.ProcessSpawningTools()
	want := map[string]bool{
		"command": true, "verify": true, "terminal_open": true, "scan_pack": true,
		"capture_page": true, "measure_page": true, "render_view": true, "page_open": true,
	}
	for _, tool := range got {
		delete(want, tool)
	}
	if len(want) != 0 {
		t.Fatalf("bundled spawns_process missing tools: %v", want)
	}
}

func TestValidateSpawnsProcessRejectsDuplicateAndUnknown(t *testing.T) {
	cfg := nativemanifest.Config{SpawnsProcess: []string{"command", "command", "missing"}}
	if err := cfg.ValidateSpawnsProcess(func(tool string) bool { return tool == "command" }); err == nil {
		t.Fatal("expected duplicate and unknown process tools to be rejected")
	}
}

func TestBundledMultiRootIsClosed(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	known := func(tool string) bool {
		if cfg.HasTool(tool) {
			return true
		}
		_, ok := cfg.ApprovalReversibility[tool]
		return ok
	}
	if err := cfg.ValidateMultiRoot(known); err != nil {
		t.Fatalf("ValidateMultiRoot: %v", err)
	}
	tools := cfg.MultiRootTools()
	if len(tools) == 0 {
		t.Fatal("multi_root classified no tools")
	}
	for _, capability := range nativemanifest.MultiRootCapabilities() {
		found := false
		for _, assigned := range tools {
			if assigned == capability {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no tool is classified %q — an unused capability is one nobody meant", capability)
		}
	}
}

// Every tool belongs to exactly one compiled bucket.
func TestMultiRootRejectsAnUnclassifiedNativeTool(t *testing.T) {
	cfg := nativemanifest.Config{
		Native:    map[string][]string{"filesystem": {"read", "write"}},
		MultiRoot: map[string][]string{"path": {"write"}},
	}
	err := cfg.ValidateMultiRoot(func(string) bool { return true })
	if err == nil {
		t.Fatal("want an error for a native tool missing from multi_root")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error should name the tool: %v", err)
	}
}

func TestMultiRootRejectsAToolClassifiedTwice(t *testing.T) {
	cfg := nativemanifest.Config{MultiRoot: map[string][]string{
		"path": {"read"},
		"none": {"read"},
	}}
	err := cfg.ValidateMultiRoot(func(string) bool { return true })
	if err == nil {
		t.Fatal("want an error for a tool classified twice")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error should name the tool: %v", err)
	}
}

func TestMultiRootRejectsAnUnknownCapability(t *testing.T) {
	cfg := nativemanifest.Config{MultiRoot: map[string][]string{"sideways": {"read"}}}
	err := cfg.ValidateMultiRoot(func(string) bool { return true })
	if err == nil {
		t.Fatal("want an error for an unknown capability id")
	}
	if !strings.Contains(err.Error(), "sideways") {
		t.Fatalf("error should name the capability: %v", err)
	}
}

func TestMultiRootRejectsAnUnknownTool(t *testing.T) {
	cfg := nativemanifest.Config{MultiRoot: map[string][]string{"path": {"no_such_tool"}}}
	err := cfg.ValidateMultiRoot(func(string) bool { return false })
	if err == nil {
		t.Fatal("want an error for an unknown tool")
	}
	if !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("error should name the tool: %v", err)
	}
}
