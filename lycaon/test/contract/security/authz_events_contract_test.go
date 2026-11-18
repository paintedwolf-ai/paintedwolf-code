package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAuthzEventEnumVocabulary(t *testing.T) {
	t.Parallel()
	wantActions := map[string]bool{
		"approval_decision":             true,
		"tool_denied":                   true,
		"capability_requested":          true,
		"capability_granted":            true,
		"capability_denied":             true,
		"capability_revoked":            true,
		"capability_applied":            true,
		"direct_ip_lease_reused":        true,
		"direct_ip_started":             true,
		"direct_ip_completed":           true,
		"direct_ip_reconstructed":       true,
		"mediated_endpoint":             true,
		"detection_resolved":            true,
		"ask_suppressed":                true,
		"secret_permission_used":        true,
		"secret_receipt":                true,
		"secret_contest":                true,
		"secret_destination_trusted":    true,
		"secret_host_composed_redacted": true,
		"secret_chat_local_release":     true,
		"content_apply_resolved":        true,
		"blueprint_approved":            true,
		"blueprint_superseded":          true,
		"blueprint_revoked":             true,
	}
	for _, a := range authzVocabulary(t, "EventAction") {
		if !wantActions[a] {
			t.Fatalf("unexpected action %q", a)
		}
		delete(wantActions, a)
	}
	if len(wantActions) != 0 {
		t.Fatalf("missing actions: %v", wantActions)
	}

	wantOutcomes := map[string]bool{
		"allowed": true,
		"denied":  true,
	}
	for _, o := range authzVocabulary(t, "EventOutcome") {
		if !wantOutcomes[o] {
			t.Fatalf("unexpected outcome %q", o)
		}
		delete(wantOutcomes, o)
	}
	if len(wantOutcomes) != 0 {
		t.Fatalf("missing outcomes: %v", wantOutcomes)
	}

	wantResolved := map[string]bool{
		"human":       true,
		"expiry":      true,
		"system_deny": true,
		"user_stop":   true,
		"host_stop":   true,
		"policy":      true,
	}
	for _, r := range authzVocabulary(t, "ResolvedBy") {
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
