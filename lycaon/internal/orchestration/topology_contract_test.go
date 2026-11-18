package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOrchestratorRunDispatchesEveryPattern(t *testing.T) {
	orch := NewOrchestratorImpl(OrchestratorDeps{})
	ctx := context.Background()

	for _, pattern := range allTopologyPatterns {
		_, err := orch.Run(ctx, RunRequest{Topology: TopologySpec{Pattern: pattern}})
		if err == nil {
			t.Fatalf("pattern %q: expected a dispatch error from a dependency-less orchestrator, got nil", pattern)
		}
		if strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("pattern %q is in the supported pattern list but Run has no dispatch case: %v", pattern, err)
		}
	}

	_, err := orch.Run(ctx, RunRequest{Topology: TopologySpec{Pattern: "no-such-pattern"}})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unknown pattern should hit the default unimplemented case, got %v", err)
	}
}

func TestAllTopologyPatternsHaveBundledExample(t *testing.T) {
	t.Parallel()
	dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))

	// Derive coverage from the shipped tree: load every bundled topology and
	// record which pattern it declares.
	entries, err := dir.List()
	testutil.FailErr(t, "read _topologies dir", err)
	exampleFor := map[TopologyPattern]string{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		spec, err := LoadTopologyFromFile(dir.Join(ent.Name()))
		testutil.FailErr(t, "load "+ent.Name(), err)
		exampleFor[spec.Pattern] = ent.Name()
	}

	// Every pattern the orchestrator dispatches must ship at least one catalog example.
	for _, pattern := range allTopologyPatterns {
		if _, ok := exampleFor[pattern]; !ok {
			t.Fatalf("no bundled _topologies/*.yaml declares pattern %q", pattern)
		}
	}
}
