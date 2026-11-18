package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// agentProcessStarts start a process for an agent's call, or feed one input
// that can start a command. Each reads the tree from disk.
var agentProcessStarts = map[string]bool{
	"StartPipelineAsync": true, "StartPipeline": true, "StartPTY": true, "RunPTYCapture": true, "WritePTY": true,
}

// agentLaunchDraftExemptions name, by file and function, a start that needs no
// draft save and why.
var agentLaunchDraftExemptions = map[string]string{}

// gitLaunchBinder is the one Git function that admits a launch: it saves
// drafts for the forms that read the working tree.
const gitLaunchBinder = "internal/tools/native/gitcmd/tree_forms.go bindLaunch"

// Every agent tool function that starts a process saves the person's drafts
// first, so no door can be added that lets a process read text a native read
// would not serve.
func TestAgentProcessStartsSaveDraftsFirst(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for key := range agentLaunchDraftExemptions {
		unused[key] = true
	}
	var offences []string
	starts, gitBinders := 0, 0
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		if gf.IsTest || !strings.HasPrefix(rel, "internal/tools/") {
			continue
		}
		toolsName := importName(gf.AST, "github.com/lycaon/lycaon/internal/tools")
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := rel + " " + fn.Name.Name
			saved, started, gitLaunch := launchDraftCalls(fn, toolsName)
			if gitLaunch != token.NoPos && strings.HasPrefix(rel, "internal/tools/native/gitcmd/") {
				gitBinders++
				if key != gitLaunchBinder {
					offences = append(offences, key+" admits a Git launch outside bindLaunch")
				} else if saved == token.NoPos {
					offences = append(offences, key+" admits a Git launch without saving drafts")
				}
			}
			for _, start := range started {
				starts++
				if _, exempt := agentLaunchDraftExemptions[key]; exempt {
					delete(unused, key)
					continue
				}
				if saved == token.NoPos || saved > start.pos {
					offences = append(offences, key+" calls "+start.name+" without saving drafts first")
				}
			}
		}
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("a process an agent starts reads the tree from disk, so its function calls tools.SaveLaunchDrafts "+
			"before the start (docs/files-live.md, the open document is the file):\n  %s", strings.Join(offences, "\n  "))
	}
	if starts == 0 || gitBinders != 1 {
		t.Errorf("the scan found %d process starts and %d Git launch binders; it no longer sees the doors it guards", starts, gitBinders)
	}
	for key := range unused {
		t.Errorf("exemption %q no longer matches a process start and should be deleted", key)
	}
}

type processStart struct {
	name string
	pos  token.Pos
}

// launchDraftCalls finds, in one function, the first draft save, every process
// start, and a Git launch admitted through owners.Launch.
func launchDraftCalls(fn *ast.FuncDecl, toolsName string) (saved token.Pos, started []processStart, gitLaunch token.Pos) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		case sel.Sel.Name == "SaveLaunchDrafts":
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == toolsName && (saved == token.NoPos || call.Pos() < saved) {
				saved = call.Pos()
			}
		case agentProcessStarts[sel.Sel.Name]:
			started = append(started, processStart{name: sel.Sel.Name, pos: call.Pos()})
		case sel.Sel.Name == "Launch":
			if owner, ok := sel.X.(*ast.SelectorExpr); ok && owner.Sel.Name == "owners" {
				gitLaunch = call.Pos()
			}
		}
		return true
	})
	return saved, started, gitLaunch
}
