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
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/settings"
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
			Tool:  "write",
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
		if !confine.ControlPlanePathDenied(path, write, "", "") {
			t.Errorf("write=%t: approval bypass opened the host control plane", write)
		}
		scratch := filepath.Join(configDir, "debug", "sessions", "mine")
		if confine.ControlPlanePathDenied(filepath.Join(scratch, "output.txt"), write, scratch, "") {
			t.Errorf("write=%t: own invocation scratch was refused", write)
		}
		if !confine.ControlPlanePathDenied(filepath.Join(configDir, "debug", "sessions", "other", "output.txt"), write, scratch, "") {
			t.Errorf("write=%t: another invocation scratch was admitted", write)
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
