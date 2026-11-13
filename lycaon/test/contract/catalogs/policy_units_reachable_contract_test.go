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
				ID        string `yaml:"id"`
				Kind      string `yaml:"kind"`
				When      string `yaml:"when"`
				Status    string `yaml:"x-paintedwolf-status"`
				Successor string `yaml:"x-paintedwolf-successor"`
			}
			testutil.FailErr(t, "parse "+rel, yaml.Unmarshal(raw, &unit))
			seen++
			if unit.Kind == "hint" {
				// A hint is never evaluated; it exists only to render copy
				// for a code the host raises ([OAR-HINT-1]).
				var entry guidance.HintEntry
				if err := yaml.Unmarshal(raw, &entry); err != nil {
					t.Errorf("%s: hint copy projection failed: %v", rel, err)
				} else if !hintViewShipsCopy(unit.ID, entry) {
					t.Errorf("%s: hint ships no copy beyond its bare code", rel)
				}
			}
			retired := strings.TrimSpace(unit.Status) == "deprecated" && strings.TrimSpace(unit.Successor) != ""
			if when := strings.TrimSpace(unit.When); when == "false" && !retired {
				t.Errorf("%s: when 'false' is unreachable by construction — the agent would receive the bare code %q", rel, unit.ID)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no policy units found under %v", dirs)
	}
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
