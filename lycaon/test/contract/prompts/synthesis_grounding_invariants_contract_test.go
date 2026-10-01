package contract

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSynthesisGroundingInvariants_lastUserTurnBoundarySkipsInternalRows(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "pkg", "api", "message_transcript.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read message_transcript.go", err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, body, 0)
	testutil.FailErr(t, "parse message_transcript.go", err)

	bodies := map[string]*ast.BlockStmt{}
	for _, decl := range f.Decls {
		candidate, ok := decl.(*ast.FuncDecl)
		if !ok || candidate.Name == nil || candidate.Body == nil {
			continue
		}
		bodies[candidate.Name.Name] = candidate.Body
	}

	// Turn boundaries and ordinals use the same user-message predicate.
	calls := func(body *ast.BlockStmt, name string) bool {
		if body == nil {
			return false
		}
		var saw bool
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == name {
					saw = true
				}
			case *ast.SelectorExpr:
				if fun.Sel != nil && fun.Sel.Name == name {
					saw = true
				}
			}
			return true
		})
		return saw
	}

	boundary := bodies["UserIntentBoundary"]
	if boundary == nil {
		t.Fatal("missing UserIntentBoundary in message_transcript.go")
	}
	switch {
	case calls(boundary, "IsInternalTranscriptMessage"):
	case calls(boundary, "IsUserIntentMessage"):
		if !calls(bodies["IsUserIntentMessage"], "IsInternalTranscriptMessage") {
			t.Fatal("IsUserIntentMessage must skip api.IsInternalTranscriptMessage rows")
		}
		if !calls(bodies["UserTurnOrdinal"], "IsUserIntentMessage") {
			t.Fatal("UserTurnOrdinal must count the same rows UserIntentBoundary slices on")
		}
	default:
		t.Fatal("UserIntentBoundary must skip api.IsInternalTranscriptMessage rows")
	}
}

func TestSynthesisGroundingInvariants_proseCitationAuditUsesCloseoutEvidenceUnion(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "guard", "prose_citation_grounding.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read prose_citation_grounding.go", err)
	src := string(body)
	if !strings.Contains(src, "guidance.UnionCloseoutEvidence") {
		t.Fatal("prose citation audit must union coordinator + worker evidence via guidance.UnionCloseoutEvidence")
	}
}

func TestSynthesisGroundingInvariants_dispatchedLegEvidenceIsObserved(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx := context.Background()
	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create parent session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "path-explorer", Prompt: "survey"})
	testutil.FailErr(t, "CreateChild", err)

	grepJSON := `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`
	_, _, err = store.CommitEvidenceToolResult(ctx, child.ID, root, "grep", map[string]any{"path": ".", "pattern": "package"}, grepJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)

	ev, err := guidance.UnionLegEvidence(ctx, store, []guidance.EvidenceLeg{{ChildSessionID: child.ID}})
	testutil.FailErr(t, "UnionLegEvidence", err)
	if !evidence.PathObserved(ev.Ledger, "src/a.go") {
		t.Fatalf("dispatched leg evidence not observed; paths=%v", evidence.ObservedPathsSorted(ev.Ledger))
	}
}
