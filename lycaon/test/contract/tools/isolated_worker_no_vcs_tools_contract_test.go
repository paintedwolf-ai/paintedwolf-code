package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Isolated profiles omit repository tools because their workspaces lack metadata.
func TestIsolatedLegProfilesGrantNoGitTools(t *testing.T) {
	t.Parallel()
	gitTools := manifestGitTools(t)
	profiles := toolProfilesByID(t)
	isolated := isolatedLegProfiles(t)
	for _, profileID := range sortedProfileIDs(isolated) {
		profile, ok := profiles[profileID]
		if !ok {
			t.Errorf("%s names unknown tool profile %q", isolated[profileID], profileID)
			continue
		}
		for _, tool := range gitTools {
			if profile.ToolAllowed(tool) {
				t.Errorf("%s runs isolated on profile %q, which grants %q — that branch copy "+
					"has no .git, so the call cannot answer", isolated[profileID], profileID, tool)
			}
		}
	}
}

// Mutation-capable workers must always receive isolated write scope.
func TestMutationCapableWorkersAreAlwaysIsolated(t *testing.T) {
	t.Parallel()
	profiles := toolProfilesByID(t)
	checked := 0
	for _, agent := range workerAgents(t) {
		profile, ok := profiles[agent.ToolProfile]
		if !ok || !prompts.ToolProfileMutationCapable(profile) {
			continue
		}
		checked++
		code := workeradmission.ValidateTaskScopeForAgent(agent.ID, api.TaskScope{Mode: api.TaskScopeModeRead})
		if code != workeradmission.TaskScopeWriteRequiredCode {
			t.Errorf("mutation-capable worker %q accepts scope.mode read (code %q) — it is no "+
				"longer guaranteed an isolated branch", agent.ID, code)
		}
	}
	if checked == 0 {
		t.Fatal("no mutation-capable worker agents — this test would assert nothing")
	}
}

// A primary-tree profile keeps the complete Git tool group.
func TestSomeNonIsolatedProfileKeepsEveryGitTool(t *testing.T) {
	t.Parallel()
	gitTools := manifestGitTools(t)
	isolated := isolatedLegProfiles(t)
	for id, profile := range toolProfilesByID(t) {
		if _, runsIsolated := isolated[id]; runsIsolated {
			continue
		}
		complete := true
		for _, tool := range gitTools {
			if !profile.ToolAllowed(tool) {
				complete = false
				break
			}
		}
		if complete {
			return
		}
	}
	t.Errorf("no profile that stays on the primary tree grants the whole git group %v — "+
		"history and revision recovery are unreachable", gitTools)
}

// source_history keeps implementer change history available in isolation.
func TestIsolatedImplementerKeepsSourceHistory(t *testing.T) {
	t.Parallel()
	profile, ok := toolProfilesByID(t)["implement"]
	if !ok {
		t.Fatal("missing tool profile \"implement\"")
	}
	if !profile.ToolAllowed("source_history") {
		t.Error("implement has no history tool at all: git cannot answer on its " +
			"branch copy and source_history is not granted")
	}
}

// isolatedLegProfiles derives profiles from worker and topology isolation.
func isolatedLegProfiles(t *testing.T) map[string]string {
	t.Helper()
	profiles := toolProfilesByID(t)
	out := map[string]string{}
	for _, agent := range workerAgents(t) {
		profile, ok := profiles[agent.ToolProfile]
		if !ok || !prompts.ToolProfileMutationCapable(profile) {
			continue
		}
		out[agent.ToolProfile] = "worker agent " + agent.ID
	}
	for path, spec := range shippedTopologies(t) {
		if !orchestration.TopologyRequiresIsolation(spec) {
			continue
		}
		for _, agentID := range topologyLegAgents(spec) {
			profileID, err := prompts.ToolProfileForAgent(agentID)
			if err != nil {
				t.Errorf("%s: leg agent %q: %v", path, agentID, err)
				continue
			}
			out[profileID] = "topology " + path + " leg " + agentID
		}
	}
	if len(out) == 0 {
		t.Fatal("no isolated leg profiles derived — this test would assert nothing")
	}
	return out
}

// topologyLegAgents returns agents from every topology pattern.
func topologyLegAgents(spec orchestration.TopologySpec) []string {
	var out []string
	if spec.FanOut != nil {
		out = append(out, spec.FanOut.ProfileID)
	}
	if spec.Pack != nil {
		out = append(out, spec.Pack.ProfileID)
	}
	if spec.Supervisor != nil {
		out = append(out, spec.Supervisor.ProfileIDs...)
	}
	if spec.Pipeline != nil {
		for _, stage := range spec.Pipeline.Stages {
			out = append(out, stage.AgentProfile)
		}
	}
	kept := out[:0]
	for _, id := range out {
		if id = strings.TrimSpace(id); id != "" {
			kept = append(kept, id)
		}
	}
	return kept
}

func manifestGitTools(t *testing.T) []string {
	t.Helper()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	group := cfg.Group("git")
	if len(group) == 0 {
		t.Fatal("native.git group is empty — this test would assert nothing")
	}
	if slices.Contains(group, "source_history") {
		t.Fatal("source_history joined the git group; it reads the host ledger, not a repository")
	}
	return group
}

func toolProfilesByID(t *testing.T) map[string]sandbox.ToolProfile {
	t.Helper()
	loaded, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "sandbox.LoadToolProfiles", err)
	out := make(map[string]sandbox.ToolProfile, len(loaded))
	for _, profile := range loaded {
		out[profile.ID] = profile
	}
	return out
}

func workerAgents(t *testing.T) []agentdef.Profile {
	t.Helper()
	loaded, err := agentdef.LoadEffective()
	contractcheck.FailErr(t, "agentdef.LoadEffective", err)
	out := make([]agentdef.Profile, 0, len(loaded))
	for _, agent := range loaded {
		if slices.Contains(agent.TopologyRoles, agentdef.TopologyRoleWorker) {
			out = append(out, agent)
		}
	}
	if len(out) == 0 {
		t.Fatal("no worker agents loaded — this test would assert nothing")
	}
	return out
}

func shippedTopologies(t *testing.T) map[string]orchestration.TopologySpec {
	t.Helper()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs")
	out := map[string]orchestration.TopologySpec{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "_topologies" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		spec, err := orchestration.ParseTopology(data)
		if err != nil {
			t.Errorf("ParseTopology %s: %v", path, err)
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		out[rel] = *spec
		return nil
	})
	contractcheck.FailErr(t, "walk topologies", err)
	if len(out) == 0 {
		t.Fatal("no shipped topologies loaded — this test would assert nothing")
	}
	return out
}

func sortedProfileIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
