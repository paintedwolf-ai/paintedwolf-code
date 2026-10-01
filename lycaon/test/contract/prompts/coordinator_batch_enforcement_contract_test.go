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

func TestCoordinatorBatchEnforcement_noLegacyBoundaryInProduction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	var hits []string
	err := filepath.Walk(lycaonRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || strings.HasSuffix(path, "_test.go") {
				if base == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "lastVisibleUserTurnBoundary") {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel)
		}
		return nil
	})
	testutil.FailErr(t, "walk lycaon for legacy boundary", err)
	if len(hits) > 0 {
		t.Fatalf("lastVisibleUserTurnBoundary must not appear in production packages:\n%s", strings.Join(hits, "\n"))
	}
}

func TestCoordinatorBatchEnforcement_queueDeferredDoesNotRenderAtQueue(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "kick", "engine.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read kick engine.go", err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, body, 0)
	testutil.FailErr(t, "parse kick engine.go", err)

	var queueFn *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "QueueDeferred" {
			continue
		}
		queueFn = fn
		break
	}
	if queueFn == nil || queueFn.Body == nil {
		t.Fatal("missing QueueDeferred in kick/engine.go")
	}

	var renderAtQueue bool
	ast.Inspect(queueFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		if sel.Sel.Name == "RenderKick" || sel.Sel.Name == "renderCoordinatorKick" {
			renderAtQueue = true
		}
		return true
	})
	if renderAtQueue {
		t.Fatal("QueueDeferred must defer RenderKick to RenderPendingNudge")
	}
}

func TestCoordinatorBatchEnforcement_loopwakeStampsBatchSeqOnKickQueue(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "loopwake", "engine.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read loopwake engine.go", err)
	src := string(body)
	if !strings.Contains(src, "emitEnv.BatchSeq") || !strings.Contains(src, "BatchSeqSet") {
		t.Fatal("loopwake must stamp live batch_seq on Envelope when queueing inform Emits")
	}
}

func TestCoordinatorBatchEnforcement_kickQueueStoresBatchSeq(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "kick", "engine.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read kick engine.go", err)
	src := string(body)
	for _, needle := range []string{"batchSeq:", "hasSeq:", "deferred: true"} {
		if !strings.Contains(src, needle) {
			t.Fatalf("kick queue item must carry %q for deferred rendering and stale-sequence fencing", needle)
		}
	}
}
