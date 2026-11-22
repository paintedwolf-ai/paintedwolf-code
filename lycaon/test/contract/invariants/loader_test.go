package invariants_test

import (
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
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
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

// Every way an invariant can be wrong fails it: a violation, a renamed anchor, an
// exemption that matches nothing, an empty or unreadable corpus, and a file
// that does not parse.
func TestEvaluateFailsClosed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files      map[string]string
		want       outcome
		violations int
	}{
		"a clean tree passes": {files: probeTree, want: passes},
		"a seeded violation fails": {
			files: with(probeTree, map[string]string{"lycaon/internal/app/clean.go": "package app\n\nimport quit \"os\"\n\nfunc clean() { quit.Exit(2) }\n"}),
			want:  violates, violations: 1,
		},
		"a renamed anchor fails": {
			files: with(probeTree, map[string]string{"lycaon/internal/door/door.go": "package door\n\nimport \"os\"\n\nfunc Quit(code int) { os.Exit(code) }\n"}),
			want:  missesAnchor,
		},
		"an exemption that matches nothing fails": {
			files: with(probeTree, map[string]string{"lycaon/internal/app/main.go": "package app\n\nfunc main() {}\n"}),
			want:  staleExemption,
		},
		"an empty corpus fails": {files: nil, want: fails},
		"an unparsable file fails": {
			files: with(probeTree, map[string]string{"lycaon/internal/app/broken.go": "package app\n\nfunc broken( {\n"}),
			want:  fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), probeRule), tc.want, tc.violations)
		})
	}
}

func TestEvaluateFailsOnAnUnreadableFile(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
	if err := os.Symlink(filepath.Join(corpus.Root(), "missing.go"), filepath.Join(corpus.Root(), "lycaon/internal/app/dangling.go")); err != nil {
		contractcheck.FailErr(t, "os.Symlink failed", err)
	}
	expect(t, invariants.Evaluate(corpus, probeRule), fails, 0)
}

// A rule table entry that cannot be checked is refused before it runs.
func TestEvaluateRefusesMalformedRules(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
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
			expect(t, invariants.Evaluate(corpus, r), fails, 0)
		})
	}
}

// Non-Go inputs fail closed the same way: a missing or empty file and an
// empty tree or glob end the check.
func TestTextSelectionsFailClosed(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, map[string]string{
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
			expect(t, res, fails, 0)
		})
	}
	ok := invariants.Invariant{Name: "reads", Anchors: []invariants.Anchor{invariants.File("docs/guide.md")}, Check: func(r *invariants.Run) {
		r.Text("docs/guide.md")
		r.Tree("lycaon-den/src", ".txt")
		r.Glob("docs/*.md")
	}}
	expect(t, invariants.Evaluate(corpus, ok), passes, 0)
}

// The Go source tree is parsed and type-checked once per corpus however
// many rules read it.
func TestCorpusLoadsGoOnce(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
	typed := invariants.Invariant{Name: "typed", Anchors: []invariants.Anchor{invariants.Package("internal/door")}, Check: func(r *invariants.Run) {
		r.TypedPackage("internal/door")
	}}
	for range 3 {
		expect(t, invariants.Evaluate(corpus, probeRule), passes, 0)
		expect(t, invariants.Evaluate(corpus, typed), passes, 0)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 1 {
		t.Fatalf("stats = %+v, want one parse and one type-check", stats)
	}
}
