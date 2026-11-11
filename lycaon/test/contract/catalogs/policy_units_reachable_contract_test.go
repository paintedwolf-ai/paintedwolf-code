package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestShippedPolicyUnitsAreReachable rejects unreachable active policy copy.
func TestShippedPolicyUnitsAreReachable(t *testing.T) {
	t.Parallel()
	dirs, err := filepath.Glob(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "*", "policy"))
	testutil.FailErr(t, "glob policy dirs", err)
	if len(dirs) == 0 {
		t.Fatal("no policy directories found in the shipped pack")
	}

	seen := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		testutil.FailErr(t, "read "+dir, err)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			rel := filepath.Join(filepath.Base(filepath.Dir(dir)), "policy", e.Name())
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			testutil.FailErr(t, "read "+rel, err)
			var unit struct {
				ID          string `yaml:"id"`
				When        string `yaml:"when"`
				Enforcement string `yaml:"enforcement"`
				Status      string `yaml:"status"`
				Successor   string `yaml:"x-paintedwolf-successor"`
				Related     []struct {
					ID   string `yaml:"id"`
					Type string `yaml:"type"`
				} `yaml:"related"`
			}
			testutil.FailErr(t, "parse "+rel, yaml.Unmarshal(raw, &unit))
			seen++
			if strings.TrimSpace(unit.Enforcement) == "off" {
				// [OAR-EVAL-11] keeps an enforcement-off unit load-checked without
				// rule evaluation; hint emissions render its copy through the
				// host-raised hint channel. The unit still ships dead copy unless
				// that channel can render something beyond the bare code.
				var entry guidance.HintEntry
				if err := yaml.Unmarshal(raw, &entry); err != nil {
					t.Errorf("%s: hint copy projection failed: %v", rel, err)
				} else if !hintViewShipsCopy(unit.ID, entry) {
					t.Errorf("%s: enforcement 'off' ships copy that can never render — delete the unit or give it a real condition", rel)
				}
			}
			retired := deprecatedWithSuccessor(unit.Status, unit.Related) ||
				(strings.TrimSpace(unit.Status) == "deprecated" && strings.TrimSpace(unit.Successor) != "")
			if when := strings.TrimSpace(unit.When); when == "false" && !retired {
				t.Errorf("%s: when 'false' is unreachable by construction — the agent would receive the bare code %q", rel, unit.ID)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no policy units found under %v", dirs)
	}
}

func deprecatedWithSuccessor(status string, related []struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"`
}) bool {
	if strings.TrimSpace(status) != "deprecated" {
		return false
	}
	for _, relation := range related {
		if strings.TrimSpace(relation.ID) != "" && strings.TrimSpace(relation.Type) == "obsolete" {
			return true
		}
	}
	return false
}

// hintViewShipsCopy reports whether the projected hint view carries copy beyond
// the bare-code fallback.
func hintViewShipsCopy(id string, entry guidance.HintEntry) bool {
	view := guidance.NormalizeRejectCodeView(id, entry)
	return view.What != id || view.Cause != "" || view.Why != "" || view.Fix != "" || view.Instead != ""
}

func TestHintViewShipsCopy(t *testing.T) {
	t.Parallel()
	if hintViewShipsCopy("X", guidance.HintEntry{}) {
		t.Error("empty entry must not count as copy")
	}
	if !hintViewShipsCopy("X", guidance.HintEntry{What: "real copy"}) {
		t.Error("what copy must count")
	}
	if !hintViewShipsCopy("X", guidance.HintEntry{Fix: "do this"}) {
		t.Error("fix copy must count")
	}
}
