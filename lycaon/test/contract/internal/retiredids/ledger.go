// Package retiredids reads docs/agent-public-retired.yaml, the ledger of
// agent-public ids that shipped and were retired.
package retiredids

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// LedgerRel is the ledger path relative to the repository root.
const LedgerRel = "docs/agent-public-retired.yaml"

// Kinds lists every id kind the ledger records.
var Kinds = []string{"hint_code", "tool_name", "rule_effect", "theme_token", "virtual_root"}

type Ledger struct {
	Retired []Entry `yaml:"retired"`
}

type Entry struct {
	ID      string `yaml:"id"`
	Kind    string `yaml:"kind"`
	Summary string `yaml:"summary"`
	Date    string `yaml:"date"`
}

// Load reads the ledger and fails on an entry without an id or a known kind.
func Load(t *testing.T) Ledger {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), LedgerRel))
	contractcheck.FailErr(t, "read "+LedgerRel, err)
	var led Ledger
	contractcheck.FailErr(t, "parse "+LedgerRel, yaml.Unmarshal(raw, &led))
	for i, e := range led.Retired {
		if strings.TrimSpace(e.ID) == "" {
			t.Fatalf("retired[%d]: id required", i)
		}
		if !slices.Contains(Kinds, e.Kind) {
			t.Fatalf("retired[%d] id=%s: kind %q want one of %v", i, e.ID, e.Kind, Kinds)
		}
	}
	return led
}

// Collisions lists ledger entries whose id is live under the same kind.
func Collisions(led Ledger, live map[string]map[string]struct{}) []Entry {
	var out []Entry
	for _, e := range led.Retired {
		if _, ok := live[e.Kind][e.ID]; ok {
			out = append(out, e)
		}
	}
	return out
}

// UnrecordedDisappearances lists ids in base that head lacks and the ledger
// does not record under kind.
func UnrecordedDisappearances(led Ledger, kind string, base, head map[string]struct{}) []string {
	recorded := map[string]bool{}
	for _, e := range led.Retired {
		if e.Kind == kind {
			recorded[e.ID] = true
		}
	}
	var out []string
	for id := range base {
		if _, ok := head[id]; ok || recorded[id] {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
