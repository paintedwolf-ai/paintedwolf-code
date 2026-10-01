package toolcontract_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestCompiledBatchContracts(t *testing.T) {
	for _, name := range []string{"read", "grep", "find", "list_dir", "git_status", "scan_query"} {
		if !mustContract(t, name).Concurrent() {
			t.Fatalf("%q should be concurrent", name)
		}
	}
	for _, name := range []string{"write", "command", "task", "workflow_advance", "scan_pack"} {
		if mustContract(t, name).Concurrent() {
			t.Fatalf("%q should be serial", name)
		}
	}
	for _, name := range []string{"web_search", "fetch_url"} {
		contract := mustContract(t, name)
		if contract.ConcurrentFor(nil) || contract.ConcurrentFor(map[string]any{"parallel": false}) {
			t.Fatalf("%q should remain serial without opt-in", name)
		}
		if !contract.ConcurrentFor(map[string]any{"parallel": true}) {
			t.Fatalf("%q should admit explicit parallel opt-in", name)
		}
		if contract.BatchConcurrencyLimit != 4 {
			t.Fatalf("%q concurrency cap = %d want 4", name, contract.BatchConcurrencyLimit)
		}
	}
}

func TestOptInBatchRequiresBothCalls(t *testing.T) {
	search := mustContract(t, "web_search")
	parallel := map[string]any{"parallel": true}
	if !toolcontract.BatchGroupableCalls("web_search", parallel, search, "web_search", parallel, search) {
		t.Fatal("two opted-in web searches should group")
	}
	if toolcontract.BatchGroupableCalls("web_search", parallel, search, "web_search", nil, search) {
		t.Fatal("an omitted opt-in must keep the pair serial")
	}
	fetch := mustContract(t, "fetch_url")
	writing := map[string]any{"parallel": true, "dest": "assets/page.bin"}
	if fetch.ConcurrentFor(writing) {
		t.Fatal("a destination-writing fetch must remain serial")
	}
}

func TestSameToolBatchIsolation(t *testing.T) {
	summarize := mustContract(t, "summarize")
	read := mustContract(t, "read")
	grep := mustContract(t, "grep")
	if !toolcontract.BatchGroupableCalls("summarize", nil, summarize, "summarize", nil, summarize) {
		t.Fatal("summarize should group with summarize")
	}
	if toolcontract.BatchGroupableCalls("summarize", nil, summarize, "read", nil, read) {
		t.Fatal("summarize must not group with read")
	}
	if !toolcontract.BatchGroupableCalls("read", nil, read, "grep", nil, grep) {
		t.Fatal("shared reads should group")
	}
}

func TestUnknownToolFailsConservative(t *testing.T) {
	if _, ok := toolcontract.Lookup("future_dynamic_tool"); ok {
		t.Fatal("unknown tool must not acquire a contract")
	}
	for _, name := range []string{" read", "read ", "READ"} {
		if _, ok := toolcontract.Lookup(name); ok {
			t.Fatalf("non-canonical tool %q acquired a contract", name)
		}
	}
}

func TestCompiledOrderAndLifecycle(t *testing.T) {
	if got := mustContract(t, "summarize").Order; got != toolcontract.TurnOrderLate {
		t.Fatalf("summarize order = %v", got)
	}
	if got := mustContract(t, "wait").Order; got != toolcontract.TurnOrderTerminal {
		t.Fatalf("wait order = %v", got)
	}
	if got := mustContract(t, "write").Lifecycle; got != toolcontract.LifecycleEffectAttempt {
		t.Fatalf("write lifecycle = %v", got)
	}
	if got := mustContract(t, "scan_pack").Lifecycle; got != toolcontract.LifecycleDurableJob {
		t.Fatalf("scan_pack lifecycle = %v", got)
	}
	if got := mustContract(t, "command").Lifecycle; got != toolcontract.LifecycleEffectAttempt {
		t.Fatalf("command lifecycle = %v", got)
	}
	if got := mustContract(t, "promote_overlay").Lifecycle; got != toolcontract.LifecycleJournaledMutation {
		t.Fatalf("promote_overlay lifecycle = %v", got)
	}
}

func TestCompiledBoundedInProcess(t *testing.T) {
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_show", "read", "write", "edit", "grep"} {
		if !mustContract(t, name).BoundedInProcess {
			t.Fatalf("%q should be bounded in process", name)
		}
	}
	for _, name := range []string{"command", "verify", "terminal_open", "wait", "http_request"} {
		if mustContract(t, name).BoundedInProcess {
			t.Fatalf("%q must not be marked bounded in process", name)
		}
	}
}

func TestCatalogVocabularyRoundTrips(t *testing.T) {
	for _, value := range []toolcontract.BatchPolicy{toolcontract.BatchSerial, toolcontract.BatchShared, toolcontract.BatchSameTool} {
		parsed, ok := toolcontract.ParseBatchPolicy(value.String())
		if !ok || parsed != value {
			t.Fatalf("batch %q parsed as %v, %v", value.String(), parsed, ok)
		}
	}
	for _, value := range []toolcontract.TurnOrder{toolcontract.TurnOrderNormal, toolcontract.TurnOrderLate, toolcontract.TurnOrderTerminal} {
		parsed, ok := toolcontract.ParseTurnOrder(value.String())
		if !ok || parsed != value {
			t.Fatalf("order %q parsed as %v, %v", value.String(), parsed, ok)
		}
	}
	for _, value := range []toolcontract.Lifecycle{
		toolcontract.LifecycleReadOnly, toolcontract.LifecycleDBTransaction,
		toolcontract.LifecycleJournaledMutation, toolcontract.LifecycleDurableJob,
		toolcontract.LifecycleEffectAttempt, toolcontract.LifecycleEphemeralControl,
	} {
		parsed, ok := toolcontract.ParseLifecycle(value.String())
		if !ok || parsed != value {
			t.Fatalf("lifecycle %q parsed as %v, %v", value.String(), parsed, ok)
		}
	}
	for name, want := range map[string]toolcontract.Capability{
		"host_resource":    toolcontract.CapabilityHostResource,
		"socket":           toolcontract.CapabilitySocket,
		"direct_ip":        toolcontract.CapabilityDirectIP,
		"local_listen":     toolcontract.CapabilityLocalListen,
		"loopback_connect": toolcontract.CapabilityLoopbackConnect,
		"terminal_capture": toolcontract.CapabilityTerminalCapture,
	} {
		got, ok := toolcontract.NamedCapability(name)
		if !ok || got != want {
			t.Fatalf("NamedCapability(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := toolcontract.NamedCapability("network"); ok {
		t.Fatal("unknown capability name must not parse")
	}
	fields := toolcontract.CapabilityRequestFields()
	if len(fields) == 0 {
		t.Fatal("CapabilityRequestFields empty")
	}
	for _, field := range fields {
		if !toolcontract.IsCapabilityRequestField(field) {
			t.Fatalf("IsCapabilityRequestField(%q) = false", field)
		}
	}
	if toolcontract.IsCapabilityRequestField("network") {
		t.Fatal("unknown request field must not parse")
	}
	for _, value := range []toolcontract.Reversibility{
		toolcontract.ReversibilityReversible, toolcontract.ReversibilityRecoverable, toolcontract.ReversibilityIrreversible,
	} {
		parsed, ok := toolcontract.ParseReversibility(value.String())
		if !ok || parsed != value {
			t.Fatalf("reversibility %q parsed as %v, %v", value.String(), parsed, ok)
		}
	}
}

func mustContract(t *testing.T, name string) toolcontract.Contract {
	t.Helper()
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		t.Fatalf("contract %q is not declared", name)
	}
	return contract
}

// A dynamic tool declares its external subsystem owner.
func TestExternalContractIsHostDeclared(t *testing.T) {
	contract := toolcontract.External("mcp:coropa")
	if contract.Owner != "mcp:coropa" {
		t.Fatalf("owner = %q, want mcp:coropa", contract.Owner)
	}
	if contract.Lifecycle != toolcontract.LifecycleEffectAttempt {
		t.Fatalf("lifecycle = %v, want effect_attempt", contract.Lifecycle)
	}
	if contract.Reversibility != toolcontract.ReversibilityRecoverable {
		t.Fatalf("reversibility = %v, want recoverable", contract.Reversibility)
	}
	if contract.Evidence() != toolcontract.EvidenceAttempt {
		t.Fatalf("evidence = %v, want attempt", contract.Evidence())
	}
	if !contract.MutatesWorld() {
		t.Fatal("an external call the host cannot bound must count as mutating")
	}
}
