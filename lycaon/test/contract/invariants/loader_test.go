package invariants_test

import (
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// probeRule bans calls to os.Exit in internal/, exempting by "file:Func".
var probeRule = invariants.Invariant{
	Name:    "probe",
	Anchors: []invariants.Anchor{invariants.Func("internal/door", "Exit")},
	Exempt:  []invariants.Exemption{{Key: "internal/app/main.go:main", Reason: "the process entry point"}},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			f.Calls(f.Syntax(), func(call *ast.CallExpr, pkg, name string) {
				if pkg == "os" && name == "Exit" && !invariants.Under(f, "internal/door") && !r.Exempt(f.Site(call.Pos())) {
					r.ReportAt(call.Pos(), "os.Exit outside the door")
				}
			})
		}
	},
}

var probeTree = map[string]string{
	"lycaon/internal/door/door.go":  "package door\n\nimport \"os\"\n\nfunc Exit(code int) { os.Exit(code) }\n",
	"lycaon/internal/app/main.go":   "package app\n\nimport \"os\"\n\nfunc main() { os.Exit(0) }\n",
	"lycaon/internal/app/clean.go":  "package app\n\nfunc clean() {}\n",
	"lycaon/internal/app/x_test.go": "package app\n\nimport \"os\"\n\nfunc helper() { os.Exit(1) }\n",
}

// Every way an invariant can be wrong invariantstest.Fails it: a violation, a renamed anchor, an
// exemption that matches nothing, an empty or unreadable corpus, and a file
// that does not parse.
func TestEvaluateFailsClosed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files      map[string]string
		want       invariantstest.Outcome
		violations int
	}{
		"a clean tree passes": {files: probeTree, want: invariantstest.Passes},
		"a seeded violation fails": {
			files: invariantstest.With(probeTree, map[string]string{"lycaon/internal/app/clean.go": "package app\n\nimport quit \"os\"\n\nfunc clean() { quit.Exit(2) }\n"}),
			want:  invariantstest.Violates, violations: 1,
		},
		"a renamed anchor fails": {
			files: invariantstest.With(probeTree, map[string]string{"lycaon/internal/door/door.go": "package door\n\nimport \"os\"\n\nfunc Quit(code int) { os.Exit(code) }\n"}),
			want:  invariantstest.MissesAnchor,
		},
		"an exemption that matches nothing fails": {
			files: invariantstest.With(probeTree, map[string]string{"lycaon/internal/app/main.go": "package app\n\nfunc main() {}\n"}),
			want:  invariantstest.StaleExemption,
		},
		"an empty corpus fails": {files: nil, want: invariantstest.Fails},
		"an unparsable file fails": {
			files: invariantstest.With(probeTree, map[string]string{"lycaon/internal/app/broken.go": "package app\n\nfunc broken( {\n"}),
			want:  invariantstest.Fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), probeRule), tc.want, tc.violations)
		})
	}
}

func TestEvaluateFailsOnAnUnreadableFile(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, probeTree)
	if err := os.Symlink(filepath.Join(corpus.Root(), "missing.go"), filepath.Join(corpus.Root(), "lycaon/internal/app/dangling.go")); err != nil {
		contractcheck.FailErr(t, "os.Symlink failed", err)
	}
	invariantstest.Expect(t, invariants.Evaluate(corpus, probeRule), invariantstest.Fails, 0)
}

// A rule table entry that cannot be checked is refused before it runs.
func TestEvaluateRefusesMalformedRules(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, probeTree)
	check := func(*invariants.Run) {}
	anchor := []invariants.Anchor{invariants.Package("internal/door")}
	for name, r := range map[string]invariants.Invariant{
		"no check":                {Name: "x", Anchors: anchor},
		"no anchor":               {Name: "x", Check: check},
		"an unreasoned exemption": {Name: "x", Anchors: anchor, Check: check, Exempt: []invariants.Exemption{{Key: "k"}}},
		"a repeated exemption": {Name: "x", Anchors: anchor, Check: check, Exempt: []invariants.Exemption{
			{Key: "k", Reason: "r"}, {Key: "k", Reason: "r"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(corpus, r), invariantstest.Fails, 0)
		})
	}
}

// Non-Go inputs fail closed the same way: a missing or empty file and an
// empty tree or glob end the check.
func TestTextSelectionsFailClosed(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, map[string]string{
		"docs/empty.md":          "  \n",
		"docs/guide.md":          "# Guide\n",
		"lycaon/internal/x/x.go": "package x\n",
		"lycaon-den/src/a.txt":   "a\n",
	})
	for name, read := range map[string]func(*invariants.Run){
		"missing file":       func(r *invariants.Run) { r.Text("docs/missing.md") },
		"empty file":         func(r *invariants.Run) { r.Text("docs/empty.md") },
		"empty tree":         func(r *invariants.Run) { r.Tree("lycaon-den/src", ".ts") },
		"no glob match":      func(r *invariants.Run) { r.Glob("docs/*.yaml") },
		"missing decl":       func(r *invariants.Run) { r.Declaration("internal/x", "Missing") },
		"missing Go file":    func(r *invariants.Run) { r.GoFile("internal/x/missing.go") },
		"empty Go selection": func(r *invariants.Run) { r.GoFiles(invariants.Production, "internal/none") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(corpus, invariants.Invariant{Name: name, Anchors: []invariants.Anchor{invariants.Package("internal/x")}, Check: read})
			invariantstest.Expect(t, res, invariantstest.Fails, 0)
		})
	}
	ok := invariants.Invariant{Name: "reads", Anchors: []invariants.Anchor{invariants.File("docs/guide.md")}, Check: func(r *invariants.Run) {
		r.Text("docs/guide.md")
		r.Tree("lycaon-den/src", ".txt")
		r.Glob("docs/*.md")
	}}
	invariantstest.Expect(t, invariants.Evaluate(corpus, ok), invariantstest.Passes, 0)
}

// The Go source tree is parsed and type-checked once per corpus however
// many rules read it.
func TestCorpusLoadsGoOnce(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, probeTree)
	typed := invariants.Invariant{Name: "typed", Anchors: []invariants.Anchor{invariants.Package("internal/door")}, Check: func(r *invariants.Run) {
		r.TypedPackage("internal/door")
	}}
	for range 3 {
		invariantstest.Expect(t, invariants.Evaluate(corpus, probeRule), invariantstest.Passes, 0)
		invariantstest.Expect(t, invariants.Evaluate(corpus, typed), invariantstest.Passes, 0)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 1 {
		t.Fatalf("stats = %+v, want one parse and one type-check", stats)
	}
}

// Anchors, import queries, and string-constant resolution read the compact
// index without loading any on-demand syntax trees into GoCorpus.Fset.
func TestCompactIndexAvoidsOnDemandASTForAnchors(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, map[string]string{
		"lycaon/internal/door/door.go": `package door

import (
	_ "embed"
	"os"
)

const Banner = "door-banner"

type Gate struct {
	Open bool
}

func (Gate) Enter() {}

func Exit(code int) { os.Exit(code) }
`,
	})
	anchorOnly := invariants.Invariant{
		Name: "anchor-only",
		Anchors: []invariants.Anchor{
			invariants.GoPath("internal/door/door.go"),
			invariants.Package("internal/door"),
			invariants.Decl("internal/door", "Gate"),
			invariants.Decl("internal/door", "Gate.Open"),
			invariants.Decl("internal/door", "Gate.Enter"),
			invariants.Func("internal/door", "Exit"),
			invariants.Named("internal/door", "Enter"),
		},
		Check: func(r *invariants.Run) {
			f := r.GoFile("internal/door/door.go")
			if !f.Imports("embed") || !f.Imports("os") || f.Imports("fmt") {
				r.Fatalf("unexpected Imports results for %s", f.Rel)
			}
			if f.PkgName != "door" {
				r.Fatalf("PkgName = %q, want door", f.PkgName)
			}
		},
	}
	invariantstest.Expect(t, invariants.Evaluate(corpus, anchorOnly), invariantstest.Passes, 0)
	if base := corpus.Go().Fset.Base(); base != 1 {
		t.Fatalf("Fset.Base() = %d, want 1 (no on-demand AST parses for anchor-only check)", base)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 0 {
		t.Fatalf("stats = %+v, want 1 GoParse and 0 TypeChecks", stats)
	}
}

// On-demand syntax loading, declaration lookups, Site resolution, ConstString,
// and TypedPackage.Syntax stay deterministic and race-free across concurrent
// and repeated in-process evaluations.
func TestOnDemandSyntaxRepeatableAndConcurrent(t *testing.T) {
	t.Parallel()
	tree := invariantstest.With(probeTree, map[string]string{
		"lycaon/internal/app/clean.go": "package app\n\nimport quit \"os\"\n\nconst tag = \"seeded\"\n\nfunc clean() { _ = tag; quit.Exit(2) }\n",
	})
	corpus := invariantstest.Fixture(t, tree)
	constRule := invariants.Invariant{
		Name:    "const-and-source",
		Anchors: []invariants.Anchor{invariants.Decl("internal/app", "tag"), invariants.Func("internal/app", "clean")},
		Check: func(r *invariants.Run) {
			f, fn := r.FuncNamed("internal/app", "clean")
			if got := f.Site(fn.Body.Pos()); got != "internal/app/clean.go:clean" {
				r.Fatalf("Site = %q, want internal/app/clean.go:clean", got)
			}
			if enc := f.EnclosingFunc(fn.Body.Pos()); enc == nil || enc.Name.Name != "clean" {
				r.Fatalf("EnclosingFunc = %v, want clean", enc)
			}
			d := r.Declaration("internal/app", "tag")
			if d.Node == nil || d.File != f {
				r.Fatalf("Declaration(tag) = %+v, want node in %s", d, f.Rel)
			}
			if val, ok := r.Go().ConstString(f, &ast.Ident{Name: "tag"}); !ok || val != "seeded" {
				r.Fatalf("ConstString(tag) = (%q, %v), want (seeded, true)", val, ok)
			}
			if src := f.Source(r.Go(), fn.Name); src != "clean" {
				r.Fatalf("Source(fn.Name) = %q, want clean", src)
			}
			if len(f.Bytes()) == 0 {
				r.Fatalf("Bytes() returned empty slice")
			}
			pkg := r.TypedPackage("internal/app")
			if len(pkg.Syntax()) != len(pkg.Files) {
				r.Fatalf("len(pkg.Syntax()) = %d, want %d", len(pkg.Syntax()), len(pkg.Files))
			}
		},
	}

	first := invariants.Evaluate(corpus, probeRule)
	invariantstest.Expect(t, first, invariantstest.Violates, 1)

	const workers = 4
	errs := make(chan string, workers*2)
	done := make(chan struct{}, workers)
	for range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 3 {
				res := invariants.Evaluate(corpus, probeRule)
				if res.String() != first.String() {
					errs <- "probeRule mismatch: " + res.String()
				}
				if cres := invariants.Evaluate(corpus, constRule); cres.Failed() {
					errs <- "constRule failed: " + cres.String()
				}
			}
		}()
	}
	for range workers {
		<-done
	}
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 1 {
		t.Fatalf("stats = %+v, want 1 GoParse and 1 TypeCheck", stats)
	}
}
