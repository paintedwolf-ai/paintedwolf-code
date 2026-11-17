package laws_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// probeRule bans calls to os.Exit in internal/, exempting by "file:Func".
var probeRule = laws.Rule{
	Name:    "probe",
	Anchors: []laws.Anchor{laws.Func("internal/door", "Exit")},
	Exempt:  []laws.Exemption{{Key: "internal/app/main.go:main", Reason: "the process entry point"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == "os" && name == "Exit" && !laws.Under(f, "internal/door") && !r.Exempt(f.Site(call.Pos())) {
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

// Every way a law can be wrong fails it: a violation, a renamed anchor, an
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
			expect(t, laws.Evaluate(fixture(t, tc.files), probeRule), tc.want, tc.violations)
		})
	}
}

func TestEvaluateFailsOnAnUnreadableFile(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
	if err := os.Symlink(filepath.Join(corpus.Root(), "missing.go"), filepath.Join(corpus.Root(), "lycaon/internal/app/dangling.go")); err != nil {
		contractcheck.FailErr(t, "os.Symlink failed", err)
	}
	expect(t, laws.Evaluate(corpus, probeRule), fails, 0)
}

// A rule table entry that cannot be checked is refused before it runs.
func TestEvaluateRefusesMalformedRules(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
	check := func(*laws.Run) {}
	anchor := []laws.Anchor{laws.Package("internal/door")}
	for name, r := range map[string]laws.Rule{
		"no check":                {Name: "x", Anchors: anchor},
		"no anchor":               {Name: "x", Check: check},
		"an unreasoned exemption": {Name: "x", Anchors: anchor, Check: check, Exempt: []laws.Exemption{{Key: "k"}}},
		"a repeated exemption": {Name: "x", Anchors: anchor, Check: check, Exempt: []laws.Exemption{
			{Key: "k", Reason: "r"}, {Key: "k", Reason: "r"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, laws.Evaluate(corpus, r), fails, 0)
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
	for name, read := range map[string]func(*laws.Run){
		"missing file":       func(r *laws.Run) { r.Text("docs/missing.md") },
		"empty file":         func(r *laws.Run) { r.Text("docs/empty.md") },
		"empty tree":         func(r *laws.Run) { r.Tree("lycaon-den/src", ".ts") },
		"no glob match":      func(r *laws.Run) { r.Glob("docs/*.yaml") },
		"missing decl":       func(r *laws.Run) { r.Declaration("internal/x", "Missing") },
		"missing Go file":    func(r *laws.Run) { r.GoFile("internal/x/missing.go") },
		"empty Go selection": func(r *laws.Run) { r.GoFiles(laws.Production, "internal/none") },
	} {
		t.Run(name, func(t *testing.T) {
			res := laws.Evaluate(corpus, laws.Rule{Name: name, Anchors: []laws.Anchor{laws.Package("internal/x")}, Check: read})
			expect(t, res, fails, 0)
		})
	}
	ok := laws.Rule{Name: "reads", Anchors: []laws.Anchor{laws.File("docs/guide.md")}, Check: func(r *laws.Run) {
		r.Text("docs/guide.md")
		r.Tree("lycaon-den/src", ".txt")
		r.Glob("docs/*.md")
	}}
	expect(t, laws.Evaluate(corpus, ok), passes, 0)
}

// The Go source tree is parsed and type-checked once per corpus however
// many rules read it.
func TestCorpusLoadsGoOnce(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, probeTree)
	typed := laws.Rule{Name: "typed", Anchors: []laws.Anchor{laws.Package("internal/door")}, Check: func(r *laws.Run) {
		r.TypedPackage("internal/door")
	}}
	for range 3 {
		expect(t, laws.Evaluate(corpus, probeRule), passes, 0)
		expect(t, laws.Evaluate(corpus, typed), passes, 0)
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 1 {
		t.Fatalf("stats = %+v, want one parse and one type-check", stats)
	}
}
