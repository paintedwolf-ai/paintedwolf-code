package invariants

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"weak"
)

func writeFixtureTree(t *testing.T, files map[string]string) *Corpus {
	t.Helper()
	root := t.TempDir()
	base := map[string]string{
		"lycaon/go.mod": "module example.com/fixture\n\ngo 1.24\n",
	}
	for rel, content := range files {
		base[rel] = content
	}
	for rel, content := range base {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return LoadFixture(root)
}

func fsetFileCount(fset *token.FileSet) int {
	n := 0
	fset.Iterate(func(*token.File) bool {
		n++
		return true
	})
	return n
}

func evictCachedAST(f *GoFile) {
	f.mu.Lock()
	f.cachedAST = weak.Pointer[ast.File]{}
	f.mu.Unlock()
}

func firstCallPos(f *GoFile, file *ast.File) token.Pos {
	var pos token.Pos
	f.Calls(file, func(call *ast.CallExpr, _, _ string) {
		if !pos.IsValid() {
			pos = call.Pos()
		}
	})
	return pos
}

// Re-parsing after explicit weak-cache eviction must not register duplicate
// token.File entries in GoCorpus.Fset, must count each per-file parse, and
// must preserve exact file:line resolution for both pre- and post-eviction
// positions under concurrent readers.
func TestGoCorpusEvictionDoesNotGrowFileSet(t *testing.T) {
	t.Parallel()
	corpus := writeFixtureTree(t, map[string]string{
		"lycaon/internal/alpha/a.go": "package alpha\n\nimport \"os\"\n\nfunc First() { os.Exit(1) }\n",
		"lycaon/internal/beta/b.go":  "package beta\n\nimport \"os\"\n\n// comment line\nfunc Second() {\n\tos.Exit(2)\n}\n",
	})
	g := corpus.Go()
	if got := fsetFileCount(g.Fset); got != 0 {
		t.Fatalf("initial Fset file count = %d, want 0 before any on-demand Syntax()", got)
	}
	f1, ok := g.File("internal/alpha/a.go")
	if !ok {
		t.Fatal("missing alpha/a.go")
	}
	f2, ok := g.File("internal/beta/b.go")
	if !ok {
		t.Fatal("missing beta/b.go")
	}

	// First on-demand parses register one token.File per GoFile (testing both
	// base == 1 for f1 and base > 1 for f2).
	ast1 := f1.Syntax()
	ast2 := f2.Syntax()
	if got := fsetFileCount(g.Fset); got != 2 {
		t.Fatalf("Fset file count after initial Syntax = %d, want 2", got)
	}
	if f1.onDemandParses.Load() != 1 || f2.onDemandParses.Load() != 1 {
		t.Fatalf("onDemandParses = (%d, %d), want (1, 1)", f1.onDemandParses.Load(), f2.onDemandParses.Load())
	}
	if f2.tokenFile.Base() <= 1 {
		t.Fatalf("f2.tokenFile.Base() = %d, want > 1 to exercise non-initial base alignment", f2.tokenFile.Base())
	}

	// Cache hits while ast1/ast2 stay live must not re-parse or add FileSet entries.
	if f1.Syntax() != ast1 || f2.Syntax() != ast2 {
		t.Fatal("expected live weak cache hit to return existing *ast.File")
	}
	if f1.onDemandParses.Load() != 1 || f2.onDemandParses.Load() != 1 {
		t.Fatalf("onDemandParses after cache hit = (%d, %d), want (1, 1)", f1.onDemandParses.Load(), f2.onDemandParses.Load())
	}

	oldPos1 := firstCallPos(f1, ast1)
	oldPos2 := firstCallPos(f2, ast2)
	wantPos1, wantSite1 := g.Position(oldPos1), f1.Site(oldPos1)
	wantPos2, wantSite2 := g.Position(oldPos2), f2.Site(oldPos2)
	if wantPos1 != "internal/alpha/a.go:5" || wantSite1 != "internal/alpha/a.go:First" {
		t.Fatalf("unexpected alpha position/site: %q / %q", wantPos1, wantSite1)
	}
	if wantPos2 != "internal/beta/b.go:7" || wantSite2 != "internal/beta/b.go:Second" {
		t.Fatalf("unexpected beta position/site: %q / %q", wantPos2, wantSite2)
	}

	// Explicitly evict and re-parse across multiple cycles.
	const cycles = 5
	var latestPos1, latestPos2 token.Pos
	for cycle := 1; cycle <= cycles; cycle++ {
		evictCachedAST(f1)
		evictCachedAST(f2)
		newAST1 := f1.Syntax()
		newAST2 := f2.Syntax()
		if got := fsetFileCount(g.Fset); got != 2 {
			t.Fatalf("cycle %d: Fset file count = %d, want 2 (no growth after eviction)", cycle, got)
		}
		wantParses := int64(1 + cycle)
		if f1.onDemandParses.Load() != wantParses || f2.onDemandParses.Load() != wantParses {
			t.Fatalf("cycle %d: onDemandParses = (%d, %d), want (%d, %d)",
				cycle, f1.onDemandParses.Load(), f2.onDemandParses.Load(), wantParses, wantParses)
		}
		latestPos1 = firstCallPos(f1, newAST1)
		latestPos2 = firstCallPos(f2, newAST2)
		if latestPos1 != oldPos1 || latestPos2 != oldPos2 {
			t.Fatalf("cycle %d: post-eviction Pos (%v, %v) != pre-eviction (%v, %v)",
				cycle, latestPos1, latestPos2, oldPos1, oldPos2)
		}
		if g.Position(latestPos1) != wantPos1 || f1.Site(latestPos1) != wantSite1 {
			t.Fatalf("cycle %d: alpha resolved to %q / %q, want %q / %q",
				cycle, g.Position(latestPos1), f1.Site(latestPos1), wantPos1, wantSite1)
		}
		if g.Position(latestPos2) != wantPos2 || f2.Site(latestPos2) != wantSite2 {
			t.Fatalf("cycle %d: beta resolved to %q / %q, want %q / %q",
				cycle, g.Position(latestPos2), f2.Site(latestPos2), wantPos2, wantSite2)
		}
	}

	// Concurrent readers resolving pre- and post-eviction positions alongside
	// concurrent evictions and re-parses.
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for range 4 {
		wg.Go(func() {
			for range 10 {
				p1 := firstCallPos(f1, f1.Syntax())
				p2 := firstCallPos(f2, f2.Syntax())
				if g.Position(oldPos1) != wantPos1 || g.Position(p1) != wantPos1 || f1.Site(p1) != wantSite1 {
					errs <- "concurrent alpha mismatch"
					return
				}
				if g.Position(oldPos2) != wantPos2 || g.Position(p2) != wantPos2 || f2.Site(p2) != wantSite2 {
					errs <- "concurrent beta mismatch"
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 10 {
			evictCachedAST(f1)
			evictCachedAST(f2)
		}
	})
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if got := fsetFileCount(g.Fset); got != 2 {
		t.Fatalf("final Fset file count = %d, want 2", got)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 {
		t.Fatalf("GoParses = %d, want 1", stats.GoParses)
	}
}

// On-demand Syntax() and Bytes() reads must fail closed if a file disappears
// or changes on disk after the Go corpus index is built, rather than silently
// returning empty bytes or empty findings.
func TestGoCorpusOnDemandErrorsFailClosed(t *testing.T) {
	t.Parallel()
	const initial = "package app\n\nimport \"os\"\n\nfunc run() { os.Exit(1) }\n"
	cases := []struct {
		name   string
		mutate func(path string) error
	}{
		{
			name:   "deleted after index",
			mutate: func(path string) error { return os.Remove(path) },
		},
		{
			name: "corrupted syntax after index",
			mutate: func(path string) error {
				return os.WriteFile(path, []byte("package app\n\nfunc run( {\n"), 0o600)
			},
		},
		{
			name: "modified valid source after index",
			mutate: func(path string) error {
				return os.WriteFile(path, []byte("package app\n\nfunc run() {}\n"), 0o600)
			},
		},
	}

	syntaxRule := Invariant{
		Name:    "syntax-consumer",
		Anchors: []Anchor{Func("internal/app", "run")},
		Check: func(r *Run) {
			f := r.GoFile("internal/app/app.go")
			f.Calls(f.Syntax(), func(call *ast.CallExpr, pkg, name string) {
				if pkg == "os" && name == "Exit" {
					r.ReportAt(call.Pos(), "os.Exit")
				}
			})
		},
	}
	bytesRule := Invariant{
		Name:    "bytes-consumer",
		Anchors: []Anchor{Func("internal/app", "run")},
		Check: func(r *Run) {
			f := r.GoFile("internal/app/app.go")
			if len(f.Bytes()) == 0 {
				r.Report("empty bytes")
			}
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			corpus := writeFixtureTree(t, map[string]string{
				"lycaon/internal/app/app.go": initial,
			})
			// Build index before mutation; verify initial clean violation detection.
			f, ok := corpus.Go().File("internal/app/app.go")
			if !ok {
				t.Fatal("missing app.go")
			}
			// Mutate disk file before any on-demand Syntax()/Bytes() read.
			if err := tc.mutate(f.Path); err != nil {
				t.Fatalf("mutate %s: %v", f.Path, err)
			}
			if res := Evaluate(corpus, syntaxRule); res.Err == nil {
				t.Fatalf("syntaxRule returned Err=nil (violations=%v), want fail-closed error", res.Violations)
			}
			if res := Evaluate(corpus, bytesRule); res.Err == nil {
				t.Fatalf("bytesRule returned Err=nil (violations=%v), want fail-closed error", res.Violations)
			}
		})
	}
}
