package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Wire facts come from the subsystem owner, not result prose.

// composerCarrierAllowedCalls permits normalization and task-envelope parsing.
var composerCarrierAllowedCalls = map[string]bool{
	"TrimSpace": true,
	"ToLower":   true,
	"HasPrefix": true,
	"Contains":  true,
	"Index":     true,
	"Unmarshal": true,
}

// Result text cannot establish a boundary refusal.
func TestWireResultLayerDoesNotConsultTheRefusalVocabulary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	banned := map[string]bool{
		"PermissionDenialText": true,
		"SandboxWriteDenial":   true,
	}
	for _, rel := range []string{
		filepath.Join("lycaon", "internal", "guidance"),
		filepath.Join("lycaon", "internal", "coordinator", "promptloop"),
	} {
		dir := filepath.Join(root, rel)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			display, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "confine" {
					return true
				}
				if !banned[sel.Sel.Name] {
					return true
				}
				t.Errorf("%s:%d: %s.%s on the wire-result path — a tool result body is not "+
					"evidence about its own call. State the outcome where it is known "+
					"(ToolInvocationOut.Facts for a tool, guidance.Refusal for a refusal).",
					display, fset.Position(call.Pos()).Line, pkg.Name, sel.Sel.Name)
				return true
			})
			return nil
		})
		testutil.FailErr(t, "walk "+rel, err)
	}
}

// ComposeToolResult carries facts without classifying body text.
func TestComposeToolResultStaysACarrier(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "guidance", "tool_result.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse tool_result.go", err)

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch pkg.Name {
		case "strings", "json":
			if !composerCarrierAllowedCalls[sel.Sel.Name] {
				t.Errorf("tool_result.go:%d: %s.%s — the composer carries facts, it does not "+
					"interpret the body", fset.Position(call.Pos()).Line, pkg.Name, sel.Sel.Name)
			}
		case "confine":
			t.Errorf("tool_result.go:%d: confine.%s — the composer must not classify the body",
				fset.Position(call.Pos()).Line, sel.Sel.Name)
		}
		return true
	})
}

// refusalVocabularyProse packs the phrases a prose matcher would reach for into
// a body whose outcome is a successful read.
const refusalVocabularyProse = `# Denying a tool call

Rejected: the host refuses a call whose Code: names a retired grant.
Denied by policy, blocked at the boundary, and refused before dispatch all
describe the same outcome. An approval that is not granted leaves the call
withheld; the error is reported to the coordinator as a structured reject.
`

// The composer carries subsystem-owner facts, so a body using the refusal
// vocabulary still settles completed with no code.
func TestRefusalVocabularyInAResultBodyIsACompletedResult(t *testing.T) {
	t.Parallel()
	tr := guidance.ComposeToolResult(refusalVocabularyProse, guidance.ToolResultFacts{}, nil)
	if tr == nil {
		t.Fatal("expected a tool result")
	}
	if tr.Outcome != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed — the body discusses refusals, it is not one", tr.Outcome)
	}
	if len(tr.Codes) != 0 {
		t.Fatalf("codes = %v want none — no producer raised one", tr.Codes)
	}
}
