package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAllTopologyPatternsHaveBundledExample(t *testing.T) {
	t.Parallel()
	dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))

	// Derive coverage from the shipped tree: load every bundled topology and
	// record which pattern it declares.
	entries, err := dir.List()
	contractcheck.FailErr(t, "read _topologies dir", err)
	exampleFor := map[orchestration.TopologyPattern]string{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		spec, err := orchestration.LoadTopologyFromFile(dir.Join(ent.Name()))
		contractcheck.FailErr(t, "load "+ent.Name(), err)
		exampleFor[spec.Pattern] = ent.Name()
	}

	// Every pattern the orchestrator dispatches must ship at least one catalog example.
	for _, pattern := range orchestration.AllTopologyPatterns() {
		if _, ok := exampleFor[pattern]; !ok {
			t.Fatalf("no bundled _topologies/*.yaml declares pattern %q (supported patterns derive from orchestration.AllTopologyPatterns)", pattern)
		}
	}
}
