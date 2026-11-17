package contract

// Installed-helper qualification stays outside the HTTP journey layer.

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// HTTP journeys live in test/security; a suite that qualifies installed bytes
// does not, so a security digest never selects it or skips around it.
func TestInstalledHelperSuiteIsNotASecurityJourney(t *testing.T) {
	t.Parallel()
	var misplaced []string
	walkTestFiles(t, contractcheck.RepoRoot(t), func(pkg string, _ *token.FileSet, _ *ast.File, src []byte) {
		if strings.HasPrefix(pkg, "./test/security") && strings.Contains(string(src), "installedhelper") {
			misplaced = append(misplaced, pkg)
		}
	})
	slices.Sort(misplaced)
	contractcheck.FailViolations(t, "installed helper qualification inside the HTTP journey layer", slices.Compact(misplaced))
}
