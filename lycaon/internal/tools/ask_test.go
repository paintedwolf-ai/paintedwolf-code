package tools_test

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestAskCatalogHasOnlyReadAndReviewedCoordinationTools(t *testing.T) {
	catalog, err := nativemanifest.Load()
	testutil.FailErr(t, "load native catalog", err)
	for _, row := range catalog.ToolContractRows() {
		contract, ok := toolcontract.Lookup(row.Tool)
		if !ok || contract.AskAllowed != row.AskAllowed {
			t.Fatalf("Ask classification for %s: %+v", row.Tool, contract)
		}
		if !contract.AskAllowed {
			continue
		}
		if contract.Lifecycle == toolcontract.LifecycleJournaledMutation {
			t.Errorf("%s has journaled mutation lifecycle in Ask", row.Tool)
		}
		if row.Tool == "command" {
			continue
		}
		if slices.Contains(catalog.SpawnsProcess, row.Tool) {
			t.Errorf("%s spawns a process in Ask", row.Tool)
		}
		protected := toolcontract.CapabilityFileChange | toolcontract.CapabilityWriteRoot | toolcontract.CapabilityProcessControl |
			toolcontract.CapabilityCredentialStore | toolcontract.CapabilityHostExecution
		if contract.Capabilities&protected != 0 {
			t.Errorf("%s has a protected effect in Ask", row.Tool)
		}
	}
	for _, allowed := range []string{"command", "command_output", "command_stop", "recall"} {
		if !tools.AllowedInAsk(tools.ToolMeta{Name: allowed}) {
			t.Errorf("AllowedInAsk(%q) = false, want true", allowed)
		}
	}
	for _, blocked := range []string{"verify", "terminal_open", "terminal_send", "terminal_read", "terminal_snapshot", "terminal_close", "code_rewrite", "restore_version"} {
		if tools.AllowedInAsk(tools.ToolMeta{Name: blocked}) {
			t.Errorf("AllowedInAsk(%q) = true, want false", blocked)
		}
	}
}

func TestAskRegistryStopsProtectedToolsBeforeHandler(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	var calls []string
	for _, name := range []string{"verify", "secret_generate", "promote_overlay", "code_rewrite", "command", "command_output", "command_stop", "recall"} {
		name := name
		testutil.FailErr(t, "register "+name, registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			calls = append(calls, name)
			return "", nil
		}))
	}
	tc := tools.ToolContext{Source: tools.InvocationSource{AskMode: true}}
	for _, name := range []string{"verify", "secret_generate", "promote_overlay", "code_rewrite"} {
		_, err := registry.Run(t.Context(), name, nil, tc)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.AskModeActiveCode {
			t.Errorf("%s reject = %v", name, err)
		}
	}
	wantAllowed := []string{"command", "command_output", "command_stop", "recall"}
	for _, name := range wantAllowed {
		_, err := registry.Run(t.Context(), name, nil, tc)
		testutil.FailErr(t, name, err)
	}
	if !slices.Equal(calls, wantAllowed) {
		t.Fatalf("handlers invoked = %v, want %v", calls, wantAllowed)
	}
}
