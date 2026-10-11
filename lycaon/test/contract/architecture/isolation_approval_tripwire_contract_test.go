package contract

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Isolation outcomes route to retry, human decision, or control-plane refusal.
func TestIsolationOutcomesAreApprovalTripwires(t *testing.T) {
	t.Parallel()

	wantCategory := map[isolation.Disposition]string{
		isolation.DispositionRetry:         "recoverable",
		isolation.DispositionHumanDecision: "user-decision",
		isolation.DispositionControlPlane:  "control-plane",
	}
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load stock guidance", err)

	registered := map[string]isolation.Outcome{}
	var terminal []string
	for _, outcome := range isolation.Outcomes() {
		if prior, duplicate := registered[outcome.Code]; duplicate {
			t.Errorf("duplicate isolation outcome %q (%s and %s)", outcome.Code, prior.Disposition, outcome.Disposition)
			continue
		}
		category, valid := wantCategory[outcome.Disposition]
		if !valid {
			t.Errorf("%s has unknown isolation disposition %q", outcome.Code, outcome.Disposition)
			continue
		}
		registered[outcome.Code] = outcome
		entry, ok := hints.HintCodes[outcome.Code]
		if !ok {
			t.Errorf("%s has no stock guidance", outcome.Code)
			continue
		}
		if entry.Category != category {
			t.Errorf("%s guidance category = %q, want %q for disposition %q",
				outcome.Code, entry.Category, category, outcome.Disposition)
		}
		if outcome.Disposition == isolation.DispositionControlPlane {
			terminal = append(terminal, outcome.Code)
		}
	}

	for code, entry := range hints.HintCodes {
		if !strings.HasPrefix(code, "SANDBOX_") &&
			!strings.HasPrefix(code, "REMOTE_PACKAGE_EXECUTION_") &&
			!strings.HasPrefix(entry.Emit, "guard:sandbox") {
			continue
		}
		if _, ok := registered[code]; !ok {
			t.Errorf("isolation guidance %s is not in isolation.Outcomes; register its disposition", code)
		}
	}
	sort.Strings(terminal)
	if len(terminal) != 2 || terminal[0] != isolation.CodeControlPlaneDenied || terminal[1] != isolation.CodeTerminalPathRefused {
		t.Fatalf("system-terminal isolation outcomes = %v, want preflight and observed control-plane refusals", terminal)
	}
}

// Production isolation codes come from one outcome registry.
func TestIsolationCodesHaveOneRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)

	const registry = "isolation/outcomes.go"
	production := make([]testcorpus.GoFile, 0, len(corpus.Files()))
	for _, file := range corpus.Files() {
		if !file.IsTest && file.Rel != registry {
			production = append(production, file)
		}
	}
	testcorpus.RequireNonEmpty(t, "production Go files outside the isolation registry", production)
	var findings []string
	for _, file := range production {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			if !strings.HasPrefix(value, "SANDBOX_") && !strings.HasPrefix(value, "REMOTE_PACKAGE_EXECUTION_") {
				return true
			}
			pos := corpus.Fset.Position(lit.Pos())
			findings = append(findings, file.Rel+":"+strconv.Itoa(pos.Line)+": "+value)
			return true
		})
	}
	if len(findings) > 0 {
		t.Fatalf("isolation codes emitted outside internal/isolation/outcomes.go:\n  %s\nregister the outcome and use its constant",
			strings.Join(findings, "\n  "))
	}
}

func TestIsolationCannotBecomeASubsystemOwner(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)

	type ownerDeclaration struct {
		Owner string
		Site  string
	}
	var owners []ownerDeclaration
	for _, file := range corpus.Files() {
		if file.Rel != "toolcontract/contracts.generated.go" {
			continue
		}
		ast.Inspect(file.AST, func(node ast.Node) bool {
			kv, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Owner" {
				return true
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			owner, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			pos := corpus.Fset.Position(lit.Pos())
			owners = append(owners, ownerDeclaration{
				Owner: owner,
				Site:  file.Rel + ":" + strconv.Itoa(pos.Line),
			})
			return true
		})
	}
	testcorpus.RequireNonEmpty(t, "generated subsystem-owner declarations", owners)
	for _, declaration := range owners {
		switch declaration.Owner {
		case "isolation", "sandbox", "confine", "confinement":
			t.Errorf("%s declares generic boundary %q as a subsystem owner", declaration.Site, declaration.Owner)
		}
	}
}

// Approval verdicts remain limited to silent handling or a human ask.
func TestApprovalDecisionPlaneCannotGrowASystemDeny(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)

	verdicts := map[string]bool{}
	for _, file := range corpus.Files() {
		if file.Rel != "gate/evaluate.go" {
			continue
		}
		for _, decl := range file.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typeName, ok := value.Type.(*ast.Ident)
				if !ok || typeName.Name != "Verdict" {
					continue
				}
				for _, name := range value.Names {
					verdicts[name.Name] = true
				}
			}
		}
	}
	if len(verdicts) != 2 || !verdicts["Silent"] || !verdicts["Ask"] {
		t.Fatalf("gate verdicts = %v, want exactly Silent and Ask; isolation cannot add a system-deny verdict", verdicts)
	}

	// The control plane is the only path-shaped hard deny left; agent policy
	// and credential paths ask instead.
	allowed := map[string]bool{
		"settings/rule_gate.go:deniedByRules":          true,
		"settings/rule_gate.go:controlPlaneDenyResult": true,
	}
	found := map[string]int{}
	var unexpected []string
	for _, file := range corpus.Files() {
		if file.IsTest {
			continue
		}
		for _, decl := range file.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := file.Rel + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok || !approvalResultLiteral(lit) || !literalBoolField(lit, "Denied") {
					return true
				}
				found[key]++
				if !allowed[key] {
					pos := corpus.Fset.Position(lit.Pos())
					unexpected = append(unexpected, file.Rel+":"+strconv.Itoa(pos.Line)+":"+fn.Name.Name)
				}
				return true
			})
		}
	}
	if len(unexpected) > 0 {
		t.Fatalf("ApprovalResult.Denied minted outside human policy/control-plane enforcement:\n  %s",
			strings.Join(unexpected, "\n  "))
	}
	for key := range allowed {
		if found[key] != 1 {
			t.Errorf("%s hard-deny constructors = %d, want 1", key, found[key])
		}
	}
}

func approvalResultLiteral(lit *ast.CompositeLit) bool {
	switch typ := lit.Type.(type) {
	case *ast.SelectorExpr:
		return typ.Sel.Name == "ApprovalResult"
	case *ast.Ident:
		return typ.Name == "ApprovalResult"
	default:
		return false
	}
}

func literalBoolField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != field {
			continue
		}
		value, ok := kv.Value.(*ast.Ident)
		return ok && value.Name == "true"
	}
	return false
}

func TestIsolationBypassReachesEffectsButNotControlPlane(t *testing.T) {
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	project := t.TempDir()
	if !confine.BypassEnabled() {
		t.Fatal("approval bypass was not recognized by the confinement boundary")
	}

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	result, err := gate.Evaluate(t.Context(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: project,
},
})
	contractcheck.FailErr(t, "evaluate bypassed action", err)
	if !result.AutoApproved() || result.Denied {
		t.Fatalf("approval bypass did not reach the effect: %+v", result)
	}

	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	for _, write := range []bool{false, true} {
		path := filepath.Join(configDir, "approvals.yaml")
		if !confine.ControlPlanePathDenied(path, write, "") {
			t.Errorf("write=%t: approval bypass opened the host control plane", write)
		}
		scratch := filepath.Join(configDir, "debug", "sessions", "mine")
		if confine.ControlPlanePathDenied(filepath.Join(scratch, "output.txt"), write, scratch) {
			t.Errorf("write=%t: own invocation scratch was refused", write)
		}
		if !confine.ControlPlanePathDenied(filepath.Join(configDir, "debug", "sessions", "other", "output.txt"), write, scratch) {
			t.Errorf("write=%t: another invocation scratch was admitted", write)
		}
	}

}

// Isolation settlement preserves the selected owner and receipt.
func TestIsolationSettlementStaysOnSelectedOwnerReceipt(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"touch", "/outside/file"}}, Contract: contract,
	})
	contractcheck.FailErr(t, "begin invocation receipt", err)
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusRejected, Invoked: false, EvidenceKind: "isolation",
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeWriteRootDenied, Disposition: string(isolation.DispositionHumanDecision),
		},
		Failure: &api.InvocationFailure{
			Code: isolation.CodeWriteRootDenied, Class: api.FailureClassIsolationRejection,
			Retryable: false, OwnerRef: contract.Owner,
		},
	})
	contractcheck.FailErr(t, "settle isolation boundary", err)
	if settled.Owner != contract.Owner || settled.Owner == "isolation" {
		t.Fatalf("isolation changed selected owner from %q to %q", contract.Owner, settled.Owner)
	}
	if settled.Invoked {
		t.Fatal("pre-owner isolation stop recorded the subsystem owner as invoked")
	}
	if settled.Failure == nil || settled.Failure.Code != isolation.CodeWriteRootDenied ||
		settled.Failure.Class != api.FailureClassIsolationRejection {
		t.Fatalf("settled isolation failure = %+v", settled.Failure)
	}
	if settled.Isolation == nil || settled.Isolation.Code != isolation.CodeWriteRootDenied ||
		settled.Isolation.Disposition != string(isolation.DispositionHumanDecision) {
		t.Fatalf("settled isolation outcome = %+v", settled.Isolation)
	}

	receipt, err = recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-2", ToolName: "command",
		Args: map[string]any{"command": []any{"touch", "/outside/file"}}, Contract: contract,
	})
	contractcheck.FailErr(t, "begin attempted-effect receipt", err)
	settled, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: string(contract.Evidence()),
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeTryWriteRoot, Disposition: string(isolation.DispositionRetry),
		},
	})
	contractcheck.FailErr(t, "settle low-level isolation observation", err)
	if settled.Owner != contract.Owner || !settled.Invoked || settled.Failure != nil ||
		settled.Isolation == nil || settled.Isolation.Code != isolation.CodeTryWriteRoot {
		t.Fatalf("low-level isolation receipt = %+v", settled)
	}
}

func TestInvocationReceiptRejectsUntypedIsolationSettlement(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	contractcheck.FailErr(t, "begin invocation receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusRejected, EvidenceKind: "rejection",
		Failure: &api.InvocationFailure{
			Code: isolation.CodeTryWriteRoot, Class: api.FailureClassHostRejection, Retryable: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "isolation settlement code/class mismatch") {
		t.Fatalf("untyped isolation settlement error = %v", err)
	}
}

func TestInvocationReceiptRejectsMalformedTerminalIsolation(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	contractcheck.FailErr(t, "begin invocation receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: string(contract.Evidence()),
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeControlPlaneDenied, Disposition: string(isolation.DispositionControlPlane),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires its rejection failure") {
		t.Fatalf("completed terminal isolation error = %v", err)
	}

	receipt, err = recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-2", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	contractcheck.FailErr(t, "begin errored terminal receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusError, Invoked: true, EvidenceKind: "error",
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeControlPlaneDenied, Disposition: string(isolation.DispositionControlPlane),
		},
		Failure: &api.InvocationFailure{
			Code: isolation.CodeControlPlaneDenied, Class: api.FailureClassIsolationRejection,
			Retryable: false, OwnerRef: contract.Owner,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a rejected receipt") {
		t.Fatalf("errored terminal isolation error = %v", err)
	}
}

// File-change and process reviews run at the owner's effect seam, after the
// owner started. Their terminal refusals settle as rejections that keep the
// owner's invoked fact instead of failing the turn.
func TestInvocationReceiptSettlesOwnerSeamTerminalRejection(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	for _, tc := range []struct {
		tool string
		code string
	}{
		{"write", isolation.CodeControlPlaneDenied},
		{"process_signal", isolation.CodeExecutionCapabilityDenied},
	} {
		contract, ok := toolcontract.Lookup(tc.tool)
		if !ok {
			t.Fatalf("%s contract is not declared", tc.tool)
		}
		outcome, ok := isolation.Lookup(tc.code)
		if !ok || outcome.Retryable() {
			t.Fatalf("%s is not a terminal isolation outcome", tc.code)
		}
		receipt, err := recorder.Begin(t.Context(), invocation.Start{
			ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
			ToolCallID: "call-" + tc.tool, ToolName: tc.tool,
			Args: map[string]any{"path": "notes.md"}, Contract: contract,
		})
		contractcheck.FailErr(t, "begin "+tc.tool+" receipt", err)
		settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
			Status: api.InvocationStatusRejected, Invoked: true, EvidenceKind: "rejection",
			Isolation: &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)},
			Failure: &api.InvocationFailure{
				Code: outcome.Code, Class: api.FailureClassIsolationRejection,
				Retryable: false, OwnerRef: contract.Owner,
			},
		})
		contractcheck.FailErr(t, "settle "+tc.tool+" owner-seam rejection", err)
		if settled.Status != api.InvocationStatusRejected || !settled.Invoked ||
			settled.Isolation == nil || settled.Isolation.Code != outcome.Code ||
			settled.Failure == nil || settled.Failure.Code != outcome.Code {
			t.Fatalf("%s owner-seam receipt = %+v", tc.tool, settled)
		}
	}
}

func TestProtectedPathsRemainDeclarableForApproval(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("home directory unavailable")
	}
	for _, path := range []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".aws", "credentials"),
		"/etc/hosts",
		filepath.Join(t.TempDir(), "outside-project"),
	} {
		for _, capability := range []string{"read_path", "write_root"} {
			request, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
				capability: path,
			}})
			if reject != nil {
				t.Errorf("%s %q was system-denied before approval: %+v", capability, path, reject)
				continue
			}
			if request == nil {
				t.Errorf("%s %q produced no capability request", capability, path)
			}
		}
	}
}
