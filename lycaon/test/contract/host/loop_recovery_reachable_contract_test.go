package contract

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Loop-recovery copy rendered for a worker leg names only tools that leg is
// offered: copy a leg cannot act on leaves it the next tool call, which is what
// the unit fired to stop.
func TestLoopRecoveryCopyIsReachableFromAWorkerLeg(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load policy units", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	tools := knownToolNames(t, profiles)

	checked := 0
	for code, entry := range cfg.HintCodes {
		if entry.Emit != "guard:doom_loop" {
			continue
		}
		for _, sc := range entry.Scenarios {
			vars := guidance.ScenarioVars(entry, sc)
			if leg, _ := vars["worker_leg"].(bool); !leg {
				continue
			}
			profileID, _ := vars["profile"].(string)
			profile, ok := byID[profileID]
			if !ok {
				t.Fatalf("%s/%s names unknown tool profile %q", code, sc.ID, profileID)
			}
			facts := oarcopy.FactsFromData(vars)
			rendered := oarcore.RenderCopy(entry.Fix, facts) + "\n" + oarcore.RenderCopy(entry.Instead, facts)
			called, _ := vars["tool"].(string)
			for _, name := range identifierTokens(rendered) {
				if tools[name] && name != called && !profile.ToolAllowed(name) {
					t.Errorf("%s/%s worker copy names %s, which profile %s does not offer", code, sc.ID, name, profileID)
				}
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no loop-recovery unit has a worker-leg scenario")
	}
}

// knownToolNames is every tool and operation a schema or profile declares.
func knownToolNames(t *testing.T, profiles []sandbox.ToolProfile) map[string]bool {
	t.Helper()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	schemas, err := toolschema.LoadSchemaDirWithOperations(dir)
	contractcheck.FailErr(t, "load tool schemas", err)
	out := map[string]bool{}
	for name := range schemas.Tools {
		out[name] = true
	}
	for _, p := range profiles {
		for name := range p.Tools {
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no tool names declared")
	}
	return out
}

func identifierTokens(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '_')
	})
}
