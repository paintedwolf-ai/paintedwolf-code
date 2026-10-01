package contract

// Test helpers share the caller's *testing.T.

import (
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestParallelTestsAreNotUsedAsHelpers(t *testing.T) {
	t.Parallel()
	byPackage := testFuncBodiesByPackage(t, contractcheck.RepoRoot(t))

	var violations []string
	for pkg, funcs := range byPackage {
		tests := map[string]bool{}
		for name := range funcs {
			if strings.HasPrefix(name, "Test") {
				tests[name] = true
			}
		}
		for caller, body := range funcs {
			for callee := range tests {
				if callee == caller || !callsIdentifier(body, callee) {
					continue
				}
				// A parallel callee panics with the caller's *testing.T.
				if strings.Contains(funcs[callee], "t.Parallel()") {
					violations = append(violations, pkg+": "+callee+
						" declares t.Parallel() but is called by "+caller+
						" — same *testing.T, so t.Parallel fires twice and panics the package")
				}
			}
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "parallel tests invoked as helpers", contractcheck.DedupeStrings(violations))
}
