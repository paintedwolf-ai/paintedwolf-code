package contract

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
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

type authzVocabularyImporter struct{ ledger *types.Package }

func (i authzVocabularyImporter) Import(path string) (*types.Package, error) {
	if path != "github.com/lycaon/lycaon/internal/authzledger" {
		return nil, fmt.Errorf("unexpected vocabulary dependency %q", path)
	}
	return i.ledger, nil
}

func authzVocabulary(t *testing.T, typeName string) []string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	ledger, err := parser.ParseFile(fset, filepath.Join(root, "lycaon/internal/authzledger/recorder.go"), nil, 0)
	contractcheck.FailErr(t, "parse ledger vocabulary", err)
	// Evaluate the declarations without importing runtime recording dependencies.
	var declarations []ast.Decl
	for _, declaration := range ledger.Decls {
		if gen, ok := declaration.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			declarations = append(declarations, gen)
		}
	}
	ledger.Decls, ledger.Imports = declarations, nil
	ledgerPackage, err := new(types.Config).Check("github.com/lycaon/lycaon/internal/authzledger", fset, []*ast.File{ledger}, nil)
	contractcheck.FailErr(t, "evaluate ledger constants", err)
	source, err := parser.ParseFile(fset, filepath.Join(root, "lycaon/internal/authzcontext/event_types.go"), nil, 0)
	contractcheck.FailErr(t, "parse event vocabulary", err)
	config := types.Config{Importer: authzVocabularyImporter{ledger: ledgerPackage}}
	vocabulary, err := config.Check("github.com/lycaon/lycaon/internal/authzcontext", fset, []*ast.File{source}, nil)
	contractcheck.FailErr(t, "evaluate event constants", err)
	var values []string
	seen := map[string]bool{}
	for _, name := range vocabulary.Scope().Names() {
		value, ok := vocabulary.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		named, ok := value.Type().(*types.Named)
		if !ok || named.Obj().Name() != typeName {
			continue
		}
		if value.Val().Kind() != constant.String {
			t.Fatalf("%s is not a string constant", name)
		}
		text := constant.StringVal(value.Val())
		if seen[text] {
			t.Fatalf("duplicate %s value %q", typeName, text)
		}
		seen[text] = true
		values = append(values, text)
	}
	if len(values) == 0 {
		t.Fatalf("no %s declarations", typeName)
	}
	return values
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
