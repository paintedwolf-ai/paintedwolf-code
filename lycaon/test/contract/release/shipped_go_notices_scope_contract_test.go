package contract

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Staged Go binaries and notice inputs must name the same package roots.
var (
	stagedGoPackagePattern  = regexp.MustCompile(`go build\b[^\n]*\s(\./cmd/[\w.-]+)`)
	noticesGoPackagePattern = regexp.MustCompile(`(?s)SHIPPED_GO_PACKAGES=\((.*?)\)`)
)

// TestNoticesCoverEveryStagedGoBinary checks package-root parity.
func TestNoticesCoverEveryStagedGoBinary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	staged := stagedGoPackages(t, contractcheck.ReadRepoFile(t, root, "scripts/stage-engine.sh"))
	if len(staged) == 0 {
		t.Fatal("scripts/stage-engine.sh: no `go build ./cmd/...` invocations found — the extraction pattern is stale")
	}

	covered := noticesGoPackages(t, contractcheck.ReadRepoFile(t, root, "scripts/licenses-notices.sh"))
	if len(covered) == 0 {
		t.Fatal("scripts/licenses-notices.sh: SHIPPED_GO_PACKAGES not found — the extraction pattern is stale")
	}

	if missing := difference(staged, covered); len(missing) > 0 {
		t.Errorf(
			"staged into the bundle but absent from SHIPPED_GO_PACKAGES in scripts/licenses-notices.sh: %s\n"+
				"Their dependencies would ship with no attribution. Add them and rerun `./task licenses:notices`.",
			strings.Join(missing, ", "),
		)
	}
	if extra := difference(covered, staged); len(extra) > 0 {
		t.Errorf(
			"listed in SHIPPED_GO_PACKAGES but not staged by scripts/stage-engine.sh: %s\n"+
				"The notices file would over-claim what the artifact contains.",
			strings.Join(extra, ", "),
		)
	}
}

func stagedGoPackages(t *testing.T, script string) []string {
	t.Helper()
	var out []string
	for _, m := range stagedGoPackagePattern.FindAllStringSubmatch(script, -1) {
		out = append(out, m[1])
	}
	return unique(out)
}

func noticesGoPackages(t *testing.T, script string) []string {
	t.Helper()
	m := noticesGoPackagePattern.FindStringSubmatch(script)
	if m == nil {
		return nil
	}
	return unique(strings.Fields(m[1]))
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func difference(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}
