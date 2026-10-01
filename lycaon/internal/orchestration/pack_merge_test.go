package orchestration

import (
	"github.com/lycaon/lycaon/internal/extpacks"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMergeFirstValidSkipsFailures(t *testing.T) {
	results := []packLegResult{
		{Index: 0, Failed: true},
		{Index: 1, Output: "map ok"},
		{Index: 2, Output: "other"},
	}
	out, err := mergeFirstValid(results)
	testutil.FailErr(t, "mergeFirstValid failed", err)
	if out != "map ok" {
		t.Fatalf("output = %q want map ok", out)
	}
}

func TestMergeFirstValidAllFailErrors(t *testing.T) {
	_, err := mergeFirstValid([]packLegResult{
		{Index: 0, Failed: true},
		{Index: 1, Failed: true},
	})
	if err == nil {
		t.Fatal("expected error when all probes fail")
	}
}

func TestMergeConsensusTwoAgree(t *testing.T) {
	results := []packLegResult{
		{Index: 0, Output: "Plan A"},
		{Index: 1, Output: "plan a"},
		{Index: 2, Output: "Plan B"},
	}
	out, err := mergeConsensus(results)
	testutil.FailErr(t, "mergeConsensus failed", err)
	if out != "Plan A" {
		t.Fatalf("output = %q", out)
	}
}

func TestMergeConsensusNoAgreementErrors(t *testing.T) {
	_, err := mergeConsensus([]packLegResult{
		{Index: 0, Output: "a"},
		{Index: 1, Output: "b"},
		{Index: 2, Output: "c"},
	})
	if err == nil {
		t.Fatal("expected consensus error")
	}
}

func TestMergeUnionConcatenates(t *testing.T) {
	results := []packLegResult{
		{Index: 0, Output: "line one"},
		{Index: 1, Output: "line two"},
		{Index: 2, Failed: true},
	}
	out, err := mergeUnionPack(results)
	testutil.FailErr(t, "mergeUnionPack failed", err)
	if out != "line one\nline two" {
		t.Fatalf("output = %q", out)
	}
}

func TestPackCountDefaultsToThree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pack-default.yaml")
	content := "id: pack-default\npattern: pack\ntask: probe\npack:\n  profile: path-explorer\n  merge_strategy: first_valid\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	spec, err := LoadTopologyFromFile(extpacks.OnDisk(path))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if effectivePackCount(*spec.Pack) != DefaultPackCount {
		t.Fatalf("count = %d want %d", effectivePackCount(*spec.Pack), DefaultPackCount)
	}
}

func TestPackCountExceedsMaxRejected(t *testing.T) {
	err := validatePackSpec(PackSpec{Count: 6, MergeStrategy: MergeFirstValid})
	if err == nil {
		t.Fatal("expected count cap error")
	}
}

func TestEffectivePackProfileDefaultsPathExplorer(t *testing.T) {
	if got := effectivePackProfile(PackSpec{}); got != ProfilePathExplorer {
		t.Fatalf("profile = %q", got)
	}
}

func TestLoadPackProbeTopology(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "pack-probe.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pattern != TopologyPack {
		t.Fatalf("pattern = %q", spec.Pattern)
	}
	if spec.Pack == nil {
		t.Fatal("missing pack spec")
	}
	if spec.Pack.ProfileID != ProfilePathExplorer {
		t.Fatalf("profile = %q", spec.Pack.ProfileID)
	}
	if effectivePackCount(*spec.Pack) != 3 {
		t.Fatalf("count = %d", effectivePackCount(*spec.Pack))
	}
	if spec.Pack.MergeStrategy != MergeFirstValid {
		t.Fatalf("merge = %q", spec.Pack.MergeStrategy)
	}
}

func TestLoadDecisionResearchTopology(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "decision-research-fanout.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pattern != TopologyFanOut {
		t.Fatalf("pattern = %q want fan_out", spec.Pattern)
	}
	if spec.FanOut == nil || len(spec.FanOut.Subtasks) != 3 {
		t.Fatalf("fan_out subtasks = %v", spec.FanOut)
	}
	for _, sub := range spec.FanOut.Subtasks {
		if strings.TrimSpace(sub) == "" {
			t.Fatal("empty fan_out subtask")
		}
	}
	if spec.FanOut.ProfileID != ProfilePathExplorer {
		t.Fatalf("profile = %q want path-explorer", spec.FanOut.ProfileID)
	}
	if strings.TrimSpace(spec.Criterion) == "" {
		t.Fatal("expected decision criterion on topology")
	}
}
