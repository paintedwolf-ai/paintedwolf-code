package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var stablePrefixMapRangePattern = regexp.MustCompile(`for\s+\w+\s*,\s*\w+\s*:=\s*range\s+(vars|surfaceVars)\b`)

func TestStablePrefixNoSilentInvalidators(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	scan, err := scanStablePrefixSurface(lycaonRoot)
	contractcheck.FailErr(t, "scan stable-prefix surface", err)
	if len(scan.Violations) > 0 {
		t.Fatalf("stable-prefix surface must not call time/uuid/rand:\n%s",
			strings.Join(scan.Violations, "\n"))
	}
	for rel := range stablePrefixScanTargets {
		path := filepath.Join(lycaonRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+rel, err)
		if stablePrefixMapRangePattern.Match(data) {
			t.Fatalf("%s must not range directly over vars/surfaceVars maps; use sorted key copy", rel)
		}
	}
}
