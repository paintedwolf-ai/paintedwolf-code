package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAuthzEventEnumVocabulary(t *testing.T) {
	t.Parallel()
	wantActions := map[authzcontext.EventAction]bool{
		authzcontext.EventActionApprovalDecision:           true,
		authzcontext.EventActionToolDenied:                 true,
		authzcontext.EventActionCapabilityRequested:        true,
		authzcontext.EventActionCapabilityGranted:          true,
		authzcontext.EventActionCapabilityDenied:           true,
		authzcontext.EventActionCapabilityRevoked:          true,
		authzcontext.EventActionCapabilityApplied:          true,
		authzcontext.EventActionDirectIPLeaseReused:        true,
		authzcontext.EventActionDirectIPStarted:            true,
		authzcontext.EventActionDirectIPCompleted:          true,
		authzcontext.EventActionDirectIPReconstructed:      true,
		authzcontext.EventActionMediatedEndpoint:           true,
		authzcontext.EventActionDetectionResolved:          true,
		authzcontext.EventActionAskSuppressed:              true,
		authzcontext.EventActionSecretPermissionUsed:       true,
		authzcontext.EventActionSecretReceipt:              true,
		authzcontext.EventActionSecretContest:              true,
		authzcontext.EventActionSecretDestinationTrusted:   true,
		authzcontext.EventActionSecretHostComposedRedacted: true,
		authzcontext.EventActionSecretChatLocalRelease:     true,
		authzcontext.EventActionContentApplyResolved:       true,
		authzcontext.EventActionBlueprintApproved:          true,
		authzcontext.EventActionBlueprintSuperseded:        true,
		authzcontext.EventActionBlueprintRevoked:           true,
	}
	for _, a := range authzcontext.AllEventActions() {
		if !wantActions[a] {
			t.Fatalf("unexpected action %q", a)
		}
		delete(wantActions, a)
	}
	if len(wantActions) != 0 {
		t.Fatalf("missing actions: %v", wantActions)
	}

	wantOutcomes := map[authzcontext.EventOutcome]bool{
		authzcontext.EventOutcomeAllowed: true,
		authzcontext.EventOutcomeDenied:  true,
	}
	for _, o := range authzcontext.AllEventOutcomes() {
		if !wantOutcomes[o] {
			t.Fatalf("unexpected outcome %q", o)
		}
		delete(wantOutcomes, o)
	}
	if len(wantOutcomes) != 0 {
		t.Fatalf("missing outcomes: %v", wantOutcomes)
	}

	wantResolved := map[authzcontext.ResolvedBy]bool{
		authzcontext.ResolvedByHuman:      true,
		authzcontext.ResolvedByExpiry:     true,
		authzcontext.ResolvedBySystemDeny: true,
		authzcontext.ResolvedByUserStop:   true,
		authzcontext.ResolvedByHostStop:   true,
		authzcontext.ResolvedByPolicy:     true,
	}
	for _, r := range authzcontext.AllResolvedBy() {
		if !wantResolved[r] {
			t.Fatalf("unexpected resolved_by %q", r)
		}
		delete(wantResolved, r)
	}
	if len(wantResolved) != 0 {
		t.Fatalf("missing resolved_by: %v", wantResolved)
	}
}

// Silent auto-allows append no ledger rows; only HITL resolution and executor policy blocks do.
func TestRuleGateDoesNotAppendAuthzEvents(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "settings", "rule_gate.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rule_gate.go: %v", err)
	}
	if strings.Contains(string(src), "authzcontext") {
		t.Fatal("rule_gate.go must not import authzcontext — silent auto-allows emit no event")
	}
}

func TestAuthzEventHookSites(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// Every checkpoint outcome seals inside the resolving transaction.
	checkCalls(t, filepath.Join(root, "lycaon", "internal", "hitl", "resolution_seal.go"),
		"AppendApprovalGateTx", "AppendCapabilityGateTx", "AppendHumanGateTx")
	checkCalls(t, filepath.Join(root, "lycaon", "internal", "hitl", "approval_resolution.go"), "AppendApprovalGateTx")
	checkCalls(t, filepath.Join(root, "lycaon", "internal", "workflow", "persistence", "blueprints.go"), "AppendHumanGateTx")
	checkCalls(t, filepath.Join(root, "lycaon", "internal", "blueprint", "approval_store.go"), "AppendHumanGateTx")
	checkCalls(t, filepath.Join(root, "lycaon", "internal", "toolexecution", "executor_tool_approval.go"), "AppendToolDenied")
}

func checkCalls(t *testing.T, path string, want ...string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		for _, name := range want {
			if sel.Sel.Name == name {
				found[name] = true
			}
		}
		return true
	})
	for _, name := range want {
		if !found[name] {
			t.Fatalf("%s must call authz ledger %s", filepath.Base(path), name)
		}
	}
}
