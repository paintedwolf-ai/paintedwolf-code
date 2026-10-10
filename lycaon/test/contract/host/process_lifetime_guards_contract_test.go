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

func TestSessionResourceDisposalStaysReachableFromProduction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	session := filepath.Join(root, "lycaon", "internal", "session")

	chats := filepath.Join(session, "chats")
	if sites := receiverCallSites(t, chats, "m", "DisposeRuntime", ""); len(sites) == 0 {
		t.Fatal("chat deletion does not dispose its runtime resource scope")
	}
	if sites := receiverCallSites(t, chats, "m.resources", "Dispose", ""); len(sites) == 0 {
		t.Fatal("chat runtime disposal does not reach the lifecycle registry")
	}
	terminal := filepath.Join(session, "coordinatorcontrol")
	if sites := receiverCallSites(t, terminal, "m.Digests", "Forget", ""); len(sites) == 0 {
		t.Fatal("terminal worker transition does not release job-keyed wake digests")
	}
	app := filepath.Join(root, "lycaon", "internal", "app")
	if sites := receiverCallSites(t, app, "", "RegisterCleanup", ""); len(sites) == 0 {
		t.Fatal("session cleanup registration is missing from app wiring")
	}
}

// Canceling a context unblocks a goroutine waiting to read and does nothing for
// one parked on a send. A collector may abandon a stream on its error path, so
// every hand-off send in the streaming path selects on ctx.Done() — a bare send
// strands the producer goroutine and holds the HTTP response body with it.
func TestStreamProducerSendsStayCancelable(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "llm")
	fset := token.NewFileSet()
	var bare []string

	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read llm package", err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		contractcheck.FailErr(t, "parse "+name, perr)
		bare = append(bare, bareStreamSends(fset, f, name)...)
	}

	if len(bare) > 0 {
		t.Fatalf("%d stream-chunk send(s) are neither inside a select on ctx.Done() "+
			"nor declared unable to block.\n"+
			"A collector that abandons the stream strands the producer on a bare send.\n  %s",
			len(bare), strings.Join(bare, "\n  "))
	}
}

// bareStreamSends reports sends on a stream-chunk channel that sit outside a
// select and cannot be shown not to block. Sends on a buffered channel made in
// the same function are exempt; unbuffered sends are not.
func bareStreamSends(fset *token.FileSet, f *ast.File, rel string) []string {
	channels := streamChunkChannels(f)
	if len(channels) == 0 {
		return nil
	}

	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if buffersItsOwnStream(fn) {
			continue
		}
		var selects []*ast.SelectStmt
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectStmt:
				selects = append(selects, node)
			case *ast.SendStmt:
				ident, ok := node.Chan.(*ast.Ident)
				if !ok || !channels[ident.Name] {
					return true
				}
				for _, sel := range selects {
					if sel.Pos() <= node.Pos() && node.End() <= sel.End() {
						return true
					}
				}
				out = append(out, rel+":"+itoaLine(fset, node.Pos())+": "+ident.Name+" <- ...")
			}
			return true
		})
	}
	return out
}

// buffersItsOwnStream reports whether fn creates its stream channel with a
// positive buffer, which is the precondition a "cannot block" declaration rests
// on.
func buffersItsOwnStream(fn *ast.FuncDecl) bool {
	buffered := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		fnIdent, ok := call.Fun.(*ast.Ident)
		if !ok || fnIdent.Name != "make" {
			return true
		}
		ch, ok := call.Args[0].(*ast.ChanType)
		if !ok {
			return true
		}
		if elem, ok := ch.Value.(*ast.Ident); !ok || elem.Name != "StreamChunk" {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if ok && lit.Kind == token.INT && lit.Value != "0" {
			buffered = true
		}
		return true
	})
	return buffered
}

// streamChunkChannels names the identifiers in a file that carry StreamChunk:
// channels made with make(chan StreamChunk, …) and parameters declared as one.
func streamChunkChannels(f *ast.File) map[string]bool {
	names := map[string]bool{}

	isStreamChunkChan := func(expr ast.Expr) bool {
		ch, ok := expr.(*ast.ChanType)
		if !ok {
			return false
		}
		ident, ok := ch.Value.(*ast.Ident)
		return ok && ident.Name == "StreamChunk"
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					continue
				}
				fnIdent, ok := call.Fun.(*ast.Ident)
				if !ok || fnIdent.Name != "make" || !isStreamChunkChan(call.Args[0]) {
					continue
				}
				if i < len(node.Lhs) {
					if lhs, ok := node.Lhs[i].(*ast.Ident); ok {
						names[lhs.Name] = true
					}
				}
			}
		case *ast.FuncType:
			if node.Params == nil {
				return true
			}
			for _, param := range node.Params.List {
				if !isStreamChunkChan(param.Type) {
					continue
				}
				for _, name := range param.Names {
					names[name.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// receiverCallSites lists non-test files under dir holding a call to name on
// receiver recv, skipping the file that declares it. An empty recv matches any
// receiver, for names that are unique across the tree.
func receiverCallSites(t *testing.T, dir, recv, name, declaredIn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if declaredIn != "" && filepath.Base(path) == declaredIn {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if recv != "" {
				if hookReceiverPath(sel.X) != recv {
					return true
				}
			}
			out = append(out, path+":"+itoaLine(fset, call.Pos()))
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk "+dir, err)
	return out
}
