package tools_test

import (
	"context"
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
		protected := toolcontract.CapabilityFileChange | toolcontract.CapabilityWriteRoot | toolcontract.CapabilityProcessControl |
			toolcontract.CapabilityCredentialStore | toolcontract.CapabilityHostExecution
		if contract.Capabilities&protected != 0 || contract.Lifecycle == toolcontract.LifecycleJournaledMutation {
			t.Errorf("%s has a protected effect in Ask", row.Tool)
		}
	}
}

func TestAskRegistryStopsProtectedToolsBeforeHandler(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	var calls []string
	for _, name := range []string{"command", "secret_generate", "promote_overlay", "code_rewrite", "recall"} {
		testutil.FailErr(t, "register "+name, registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			calls = append(calls, name)
			return "", nil
		}))
	}
	tc := tools.ToolContext{Source: tools.InvocationSource{AskMode: true}}
	for _, name := range []string{"command", "secret_generate", "promote_overlay", "code_rewrite"} {
		_, err := registry.Run(t.Context(), name, nil, tc)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.AskModeActiveCode {
			t.Errorf("%s reject = %v", name, err)
		}
	}
	_, err := registry.Run(t.Context(), "recall", nil, tc)
	testutil.FailErr(t, "recall", err)
	if len(calls) != 1 || calls[0] != "recall" {
		t.Fatalf("handlers invoked = %v", calls)
	}
}
