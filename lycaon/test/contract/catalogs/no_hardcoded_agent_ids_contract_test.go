package contract

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestBundledSkillsDoNotHardcodeAgentIDs keeps worker-role copy out of skill bodies:
// implement-spawn.md injects the live roster, so an id in a body is a second source of
// truth that drifts into DISALLOWED_AGENT. Subjects are the spawn allowlist only;
// non-spawnable ids such as coordinator are ordinary words.
func TestBundledSkillsDoNotHardcodeAgentIDs(t *testing.T) {
	t.Parallel()
	ids := spawn.AmbientAllowedAgents()
	if len(ids) == 0 {
		t.Fatal("expected a non-empty implement-default spawn allowlist")
	}

	var failed []string
	for _, dir := range stockSkillDirs(t) {
		entries, err := dir.List()
		testutil.FailErr(t, "list skills dir", err)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			raw, err := dir.Join(entry.Name(), "SKILL.md").Read()
			if err != nil {
				continue // not every subdirectory carries a SKILL.md
			}
			for _, id := range ids {
				for _, line := range linesNamingID(string(raw), id) {
					failed = append(failed, entry.Name()+" names "+id+": "+line)
				}
			}
		}
	}

	if len(failed) > 0 {
		t.Fatalf("skill bodies naming a task() agent id (%d) — use the injected spawn roster instead:\n  - %s",
			len(failed), strings.Join(failed, "\n  - "))
	}
}

// stockSkillDirs returns every stock pack skills/ directory.
func stockSkillDirs(t *testing.T) []extpacks.Source {
	t.Helper()
	packs, err := extpacks.DiscoverStock()
	testutil.FailErr(t, "DiscoverStock", err)
	return extpacks.KindDirs(packs, "skills")
}

// linesNamingID returns trimmed body lines mentioning id as a whole word, so a longer
// hyphenated term does not match on a substring. Fenced examples are not exempt — a
// copyable dispatch snippet is the likeliest place a stale id survives.
func linesNamingID(body, id string) []string {
	pattern := regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(id) + `([^a-z0-9_-]|$)`)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if pattern.MatchString(line) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}
