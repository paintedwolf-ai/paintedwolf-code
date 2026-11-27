// Package invariantstest builds fixture repositories and checks rule
// outcomes for invariant family tests.
package invariantstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// FixtureModule is the lycaon/go.mod a fixture gets unless it supplies one.
const FixtureModule = "module github.com/lycaon/lycaon\n\ngo 1.26\n"

// Fixture writes a repository tree, keyed by repository-relative path, and
// returns a corpus over it. lycaon/go.mod is added unless given; a nil tree
// holds nothing else.
func Fixture(t *testing.T, files map[string]string) *invariants.Corpus {
	t.Helper()
	root := t.TempDir()
	files = With(files, nil)
	if _, ok := files["lycaon/go.mod"]; !ok {
		files["lycaon/go.mod"] = FixtureModule
	}
	// Rules select whole module roots and refuse an empty one.
	for _, dir := range []string{"cmd", "pkg", "config", "test"} {
		if len(files) > 1 && !hasPrefix(files, "lycaon/"+dir+"/") {
			files["lycaon/"+dir+"/placeholder/placeholder.go"] = "package placeholder\n"
		}
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			contractcheck.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			contractcheck.FailErr(t, "write file", err)
		}
	}
	return invariants.LoadFixture(root)
}

func hasPrefix(files map[string]string, prefix string) bool {
	for rel := range files {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// With returns files plus overrides; an empty override deletes the path.
func With(files map[string]string, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(files)+len(overrides))
	for rel, body := range files {
		out[rel] = body
	}
	for rel, body := range overrides {
		if body == "" {
			delete(out, rel)
			continue
		}
		out[rel] = body
	}
	return out
}

// Rule returns a rule of a family by name.
func Rule(t *testing.T, family invariants.Family, name string) invariants.Invariant {
	t.Helper()
	for _, r := range family.Invariants {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("family %s has no rule %s", family.Name, name)
	return invariants.Invariant{}
}

// Bare returns rule without its exemptions, which name repository files a
// fixture does not hold.
func Bare(r invariants.Invariant) invariants.Invariant {
	r.Exempt = nil
	return r
}

// Outcome names what an evaluation found, for table tests.
type Outcome int

const (
	Passes Outcome = iota
	Violates
	MissesAnchor
	StaleExemption
	Fails
)

func (o Outcome) String() string {
	return [...]string{"passes", "violates", "misses an anchor", "has a stale exemption", "fails to load"}[o]
}

// Expect asserts the single kind of result an evaluation produced and, for
// violations, how many.
func Expect(t *testing.T, res invariants.Result, want Outcome, violations int) {
	t.Helper()
	got := Passes
	switch {
	case res.Err != nil:
		got = Fails
	case len(res.MissingAnchors) > 0:
		got = MissesAnchor
	case len(res.StaleExemptions) > 0:
		got = StaleExemption
	case len(res.Violations) > 0:
		got = Violates
	}
	if got != want {
		t.Fatalf("rule %s, want it to %s:\n%s", got, want, res)
	}
	if want == Violates && violations > 0 && len(res.Violations) != violations {
		t.Fatalf("%d violations, want %d:\n%s", len(res.Violations), violations, strings.Join(res.Violations, "\n"))
	}
}
