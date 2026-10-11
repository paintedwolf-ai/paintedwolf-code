package sourcefeed_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

type emitSite struct {
	relFile string
	fn      string
	call    string
}

var userMutationSites = []emitSite{
	{relFile: "internal/projectsource/source_mutation_commit.go", fn: "commitTx", call: "EmitBatchTx"},
	{relFile: "internal/projectsource/source_mutation_commit.go", fn: "commitWithoutStore", call: "EmitBatch"},
}

var expectedEmitSites = map[sourcefeed.EmitDoor][]emitSite{
	sourcefeed.DoorAgentMutation: {
		{relFile: "internal/tools/native/source_write.go", fn: "commitAgentMutationTx", call: "EmitTx"},
		{relFile: "internal/tools/native/source_write.go", fn: "commitAgentMutationWithoutStore", call: "Emit"},
	},
	sourcefeed.DoorEditorSave: {{relFile: "internal/editordoc/save_publication.go", fn: "commit", call: "EmitTx"}},
	sourcefeed.DoorWorkerPromotion: {
		{relFile: "internal/worker/sql_promotion.go", fn: "CommitPromotion", call: "EmitBatchTx"},
	},
	sourcefeed.DoorUserPut:      userMutationSites,
	sourcefeed.DoorUserPost:     userMutationSites,
	sourcefeed.DoorRename:       userMutationSites,
	sourcefeed.DoorDelete:       userMutationSites,
	sourcefeed.DoorCopy:         userMutationSites,
	sourcefeed.DoorReplaceApply: userMutationSites,
}

func TestEmitDoorRegistration(t *testing.T) {
	doors := sourcefeed.AllEmitDoors()
	if len(doors) != len(expectedEmitSites) {
		t.Fatalf("AllEmitDoors=%d expectedEmitSites=%d — keep them in lockstep", len(doors), len(expectedEmitSites))
	}
	for _, d := range doors {
		if _, ok := expectedEmitSites[d]; !ok {
			t.Fatalf("door %q missing from expectedEmitSites", d)
		}
	}
	root := configlayout.FindModuleRoot()
	for door, sites := range expectedEmitSites {
		for _, site := range sites {
			path := filepath.Join(root, site.relFile)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: read %s: %v", door, path, err)
			}
			if !funcCallsSourcefeedEmit(t, path, string(body), site.fn, site.call) {
				t.Fatalf("door %q: %s in %s must call sourcefeed.%s",
					door, site.fn, site.relFile, site.call)
			}
		}
	}
}

func funcCallsSourcefeedEmit(t *testing.T, path, src, fnName, call string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != fnName {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			node, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != call {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "sourcefeed" {
				found = true
				return false
			}
			return true
		})
		return found
	}
	return false
}

func TestEmitDoorIDsAreQualified(t *testing.T) {
	for _, d := range sourcefeed.AllEmitDoors() {
		if !strings.Contains(string(d), ".") {
			t.Fatalf("door %q should be family.name", d)
		}
	}
}
