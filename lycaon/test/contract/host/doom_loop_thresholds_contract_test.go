package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/loopguard"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const doomLoopPolicyDir = "lycaon/config/packs/painted-wolf/security/policy"

type doomLoopPolicyUnit struct {
	Effect string `yaml:"effect"`
	Anchor string `yaml:"anchor"`
	When   string `yaml:"when"`
}

func readDoomLoopPolicy(t *testing.T, root, name string) doomLoopPolicyUnit {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, doomLoopPolicyDir, name))
	contractcheck.FailErr(t, "read "+name, err)
	var unit doomLoopPolicyUnit
	contractcheck.FailErr(t, "parse "+name, yaml.Unmarshal(raw, &unit))
	return unit
}

var doomLoopWarnFloor = regexp.MustCompile(`paintedwolf\.repeat_count >= (\d+)`)

// TestDoomLoopThresholdsMatchPolicy keeps the counters the Go guard blocks on
// equal to the policy units that fire on them. The warn threshold exists only in
// its policy unit.
func TestDoomLoopThresholdsMatchPolicy(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	warn := readDoomLoopPolicy(t, root, "DOOM_LOOP_REPEAT_WARN.yaml")
	floor := doomLoopWarnFloor.FindStringSubmatch(warn.When)
	if floor == nil {
		t.Fatalf("DOOM_LOOP_REPEAT_WARN declares no repeat_count floor: when=%q", warn.When)
	}
	warnAt, err := strconv.Atoi(floor[1])
	contractcheck.FailErr(t, "parse DOOM_LOOP_REPEAT_WARN floor", err)
	if !strings.Contains(warn.When, "paintedwolf.repeat_count <= "+strconv.Itoa(loopguard.DoomLoopMaxAttempts)) {
		t.Errorf("DOOM_LOOP_REPEAT_WARN stops warning at a different count than DoomLoopMaxAttempts=%d: when=%q",
			loopguard.DoomLoopMaxAttempts, warn.When)
	}

	block := readDoomLoopPolicy(t, root, "DOOM_LOOP_REPEAT.yaml")
	if !strings.Contains(block.When, "paintedwolf.repeat_count >= "+strconv.Itoa(loopguard.DoomLoopMaxAttempts)) {
		t.Errorf("DOOM_LOOP_REPEAT blocks at a different count than DoomLoopMaxAttempts=%d: when=%q",
			loopguard.DoomLoopMaxAttempts, block.When)
	}
	if warnAt >= loopguard.DoomLoopMaxAttempts {
		t.Fatalf("warn threshold %d must sit below the block at %d, or the warn can never precede it",
			warnAt, loopguard.DoomLoopMaxAttempts)
	}

	codeRepeat := readDoomLoopPolicy(t, root, "DOOM_LOOP_CODE_REPEAT.yaml")
	if !strings.Contains(codeRepeat.When, "paintedwolf.code_reject_responses >= "+strconv.Itoa(loopguard.DoomLoopMaxCodeRepeats)) {
		t.Errorf("DOOM_LOOP_CODE_REPEAT fires at a different count than DoomLoopMaxCodeRepeats=%d: when=%q",
			loopguard.DoomLoopMaxCodeRepeats, codeRepeat.When)
	}
	// The per-Code escalation exists to fire on the arcs the identical-args block
	// cannot see, so it has to be reachable well before that block would land.
	// At or above DoomLoopMaxAttempts it would only ever fire second, on calls
	// the other rule had already stopped.
	if loopguard.DoomLoopMaxCodeRepeats >= loopguard.DoomLoopMaxAttempts {
		t.Fatalf("per-Code escalation at %d must sit below the identical-args block at %d, or it can never fire first",
			loopguard.DoomLoopMaxCodeRepeats, loopguard.DoomLoopMaxAttempts)
	}
}
