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

// Every identity-keyed process store declares its retention bound.
var retentionScanPackages = []string{
	filepath.Join("internal", "session"),
	filepath.Join("internal", "llm"),
	filepath.Join("internal", "coordinator", "assembly"),
	filepath.Join("internal", "tools", "native"),
	filepath.Join("internal", "settings"),
	filepath.Join("internal", "secretharvest"),
	filepath.Join("internal", "grantedpath"),
}

// retentionAnswer classifies stores without a type-level bound.
type retentionAnswer string

const (
	retentionReleased  retentionAnswer = "Released"
	retentionJustified retentionAnswer = "Justified"
)

// retentionAnswers records released and process-lifetime stores.
var retentionAnswers = map[string]retentionAnswer{
	"lycaon/internal/session/loopback_provenance.go#sessionContainers": retentionReleased,
	"lycaon/internal/session/stream/hub.go#subs":                       retentionReleased,
	"lycaon/internal/session/stream/hub.go#sessionMsg":                 retentionReleased,
	"lycaon/internal/session/stream/hub.go#pending":                    retentionReleased,
	"lycaon/internal/session/stream/hub.go#timers":                     retentionReleased,
	"lycaon/internal/session/stream/active.go#bySession":               retentionReleased,
	"lycaon/internal/session/stream/active.go#tokens":                  retentionReleased,
	"lycaon/internal/session/checkpoint/capture.go#bySession":          retentionReleased,
	"lycaon/internal/session/turn_lifecycle.go#bySession":              retentionReleased,
	"lycaon/internal/session/promote_path_status.go#byJob":             retentionReleased,
	"lycaon/internal/session/promote_path_status.go#previewJob":        retentionReleased,
	"lycaon/internal/session/tool_approval_coalesce.go#byChat":         retentionReleased,
	"lycaon/internal/session/worker_touch_ledger.go#byJob":             retentionReleased,
	"lycaon/internal/session/promptstate/state.go#Prompt":              retentionJustified,
	"lycaon/internal/session/prompt_curation.go#bySession":             retentionReleased,
	"lycaon/internal/session/queue_round_drain.go#bySession":           retentionReleased,
	"lycaon/internal/session/store/memory.go#sessions":                 retentionJustified,
	"lycaon/internal/session/store/memory.go#messages":                 retentionJustified,
	"lycaon/internal/session/store/memory.go#untrustedRecords":         retentionJustified,
	"lycaon/internal/session/store/memory.go#secretExposureRecords":    retentionJustified,
	"lycaon/internal/secretharvest/secretharvest.go#bySession":         retentionJustified,
	// Chat approvals: durable projections of chat_grants, released when the chat is disposed.
	"lycaon/internal/session/approvalstate/sandbox_ask_guard.go#bySession": retentionReleased,
	"lycaon/internal/settings/session_grants.go#bySession":                 retentionReleased,
	"lycaon/internal/settings/ask_quiet.go#byChat":                         retentionReleased,
	"lycaon/internal/grantedpath/grantedpath.go#byRoot":                    retentionReleased,
}

// retentionIdentityKeys identify runtime identity domains.
var retentionIdentityKeys = []string{
	"session", "Session",
	"job", "Job",
	"message", "Message",
	"chat", "Chat",
}

func TestRetentionClosedSetIsDeclared(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var undeclared []string

	for _, pkg := range retentionScanPackages {
		dir := filepath.Join(root, "lycaon", pkg)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			rel, _ := filepath.Rel(root, path)
			undeclared = append(undeclared, scanRetentionStores(fset, f, rel, retentionAnswers)...)
			return nil
		})
		contractcheck.FailErr(t, "walk "+pkg, err)
	}

	if len(undeclared) > 0 {
		t.Fatalf("Retention closed set: %d store(s) keyed by runtime identity carry no "+
			"Bounded | Released | Justified answer.\n"+
			"Add each to retentionAnswers in this file — see docs/session.md "+
			"§ Process-lifetime retention.\n  %s",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
}

// scanRetentionStores reports identity stores without a retention answer.
func scanRetentionStores(fset *token.FileSet, f *ast.File, rel string, answers map[string]retentionAnswer) []string {
	var out []string

	check := func(name string, typ ast.Expr, pos token.Pos) {
		if !retentionStoreType(typ) {
			return
		}
		key := filepath.ToSlash(rel) + "#" + name
		_, answered := answers[key]
		if !retentionIdentityKeyed(name, typ) && !answered {
			return
		}
		// A scoped LRU is bounded by construction.
		if sel, ok := unwrapSelector(typ); ok && sel == "scopedstore.LRU" {
			return
		}
		if answered {
			return
		}
		out = append(out, key+" ("+rel+":"+itoaLine(fset, pos)+")")
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.StructType:
			for _, field := range node.Fields.List {
				for _, name := range field.Names {
					check(name.Name, field.Type, name.Pos())
				}
			}
		case *ast.GenDecl:
			if node.Tok != token.VAR {
				return true
			}
			for _, spec := range node.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					continue
				}
				for _, name := range vs.Names {
					check(name.Name, vs.Type, name.Pos())
				}
			}
		}
		return true
	})
	return out
}

// retentionStoreType reports supported keyed store types.
func retentionStoreType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.MapType:
		return true
	case *ast.StarExpr:
		return retentionStoreType(t.X)
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return (pkg.Name == "sync" && t.Sel.Name == "Map") ||
			(pkg.Name == "scopedstore" && (t.Sel.Name == "LRU" || t.Sel.Name == "Map"))
	case *ast.IndexExpr:
		return retentionStoreType(t.X)
	}
	return false
}

// retentionIdentityKeyed reports runtime identity domains.
func retentionIdentityKeyed(name string, expr ast.Expr) bool {
	// A scoped LRU declares a bounded runtime store.
	if sel, ok := unwrapSelector(expr); ok && sel == "scopedstore.LRU" {
		return true
	}
	for _, key := range retentionIdentityKeys {
		if strings.Contains(name, key) {
			return true
		}
	}
	return false
}

func unwrapSelector(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return unwrapSelector(t.X)
	case *ast.IndexExpr:
		return unwrapSelector(t.X)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name, true
		}
	}
	return "", false
}

func itoaLine(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return strings.TrimSpace(strings.TrimPrefix(p.String(), p.Filename+":"))
}
