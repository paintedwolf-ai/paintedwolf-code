package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Prose guard rejects retract the blocked answer and append internal steering.
func TestGuardRejectUsesBlockedAssistantTurnSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop", "reject.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read promptloop/reject.go", err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, body, 0)
	testutil.FailErr(t, "parse promptloop/reject.go", err)

	var rejectFn *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "rejectBlockedAssistantTurn" {
			continue
		}
		rejectFn = fn
		break
	}
	if rejectFn == nil {
		t.Fatal("missing rejectBlockedAssistantTurn in promptloop/reject.go")
	}
	callees := map[string]struct{}{}
	ast.Inspect(rejectFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		callees[sel.Sel.Name] = struct{}{}
		return true
	})
	for _, required := range []string{"retractRejectedAssistantTurn", "appendHostNudge"} {
		if _, ok := callees[required]; !ok {
			t.Fatalf("rejectBlockedAssistantTurn must call %s", required)
		}
	}

	pkgDir := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop")
	var src strings.Builder
	src.Write(body)
	for _, name := range []string{"loop.go", "batch.go", "reject.go", "run_stages.go"} {
		part, err := os.ReadFile(filepath.Join(pkgDir, name))
		testutil.FailErr(t, "read promptloop/"+name, err)
		src.Write(part)
	}
	requiredRef := "rejectBlockedAssistantTurn(ctx, sessionID, history, assistantMessageID, draftSlotID, reject, st)"
	if !strings.Contains(src.String(), requiredRef) {
		t.Fatalf("rejectBlockedAssistantTurn SSOT not wired from guard paths: missing %q", requiredRef)
	}

	// Guard nudges are model steering, never chat rows: appendUserNudge hardcodes
	// internal visibility, so no guard path may opt a nudge into the transcript.
	for _, name := range []string{"closeout_commit.go", "reject.go", "batch.go", "tools.go"} {
		part, err := os.ReadFile(filepath.Join(pkgDir, name))
		testutil.FailErr(t, "read promptloop/"+name, err)
		if strings.Contains(string(part), "api.MessageVisibilityTranscript") {
			t.Fatalf("promptloop/%s must not append transcript-visible nudges — guard kicks never render in chat", name)
		}
	}
}
