package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// coordinatorOnlyReferents name machinery that exists on the coordinator surface
// and nowhere else. A delegated leg has no workflow run, no phase, no progress
// checklist, and no user to ask.
var coordinatorOnlyReferents = []string{
	"Phase exit",
	"workflow Phase",
	"update_progress",
	"## Progress",
	"ask the user",
}

// workerExits are the moves a worker can actually make when a tool arc is dead.
// The completion report is the only one that ends the leg.
var workerExits = []string{
	"leg_status",
	"request_decision",
}

type loopPolicyCopy struct {
	ID   string `yaml:"id"`
	Copy struct {
		Fix     string `yaml:"fix"`
		Instead string `yaml:"instead"`
	} `yaml:"copy"`
}

// TestLoopRecoveryCopyIsReachableFromAWorkerLeg requires any loop-recovery unit
// naming coordinator-only machinery to also name, on its worker branch, an exit a
// leg can take alone. Recovery copy a leg cannot act on leaves it one move — the
// next tool call, which is what the unit fired to stop.
func TestLoopRecoveryCopyIsReachableFromAWorkerLeg(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, doomLoopPolicyDir)
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read policy dir", err)

	checked := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "DOOM_LOOP_") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		contractcheck.FailErr(t, "read "+entry.Name(), err)
		var unit loopPolicyCopy
		contractcheck.FailErr(t, "parse "+entry.Name(), yaml.Unmarshal(raw, &unit))
		copyText := unit.Copy.Fix + "\n" + unit.Copy.Instead
		named := namedReferents(copyText, coordinatorOnlyReferents)
		if len(named) == 0 {
			continue
		}
		checked++
		if !strings.Contains(copyText, "worker_leg") {
			t.Errorf("%s names coordinator-only machinery %v with no worker branch: "+
				"a delegated leg reading this has nothing to act on",
				entry.Name(), named)
			continue
		}
		if len(namedReferents(copyText, workerExits)) == 0 {
			t.Errorf("%s branches on worker_leg but never names a worker exit (%v): "+
				"the branch has to end somewhere the leg can go",
				entry.Name(), workerExits)
		}
	}
	if checked == 0 {
		t.Fatal("no loop-recovery unit named coordinator machinery — this test has " +
			"stopped watching anything; re-point it or delete it")
	}
}

func namedReferents(text string, needles []string) []string {
	var out []string
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			out = append(out, needle)
		}
	}
	return out
}
