package contract

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type workflowPhaseManifest struct {
	ID            string   `yaml:"id"`
	AllowedAgents []string `yaml:"allowed_agents"`
	Phases        []struct {
		ID string `yaml:"id"`
	} `yaml:"phases"`
}

// workflowPhaseHostOnly lists topology/host-driven phase ids that need no persona substring.
var workflowPhaseHostOnly = map[string]struct{}{
	"boot": {}, "intake": {}, "expand": {}, "plan": {}, "execute": {},
	"fan_out": {}, "hunt": {}, "approve": {}, "review_test": {},
}

func TestWorkflowPhasesCoveredByAgentPromptCorpus(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	root := contractcheck.RepoRoot(t)
	workflowsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	contractcheck.FailErr(t, "read workflows dir", err)

	corpus := buildAgentPromptCorpus(t, root)
	var violations []string

	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), "_") {
			continue
		}
		name := ent.Name()
		manifestPath := filepath.Join(workflowsDir, name, "workflow.yaml")
		raw, err := os.ReadFile(manifestPath)
		contractcheck.FailErr(t, "read "+manifestPath, err)
		var wf workflowPhaseManifest
		contractcheck.FailErr(t, "parse "+name, yaml.Unmarshal(raw, &wf))
		if len(wf.Phases) == 0 {
			continue
		}
		for _, phase := range wf.Phases {
			phaseID := strings.TrimSpace(phase.ID)
			if phaseID == "" {
				continue
			}
			if _, hostOnly := workflowPhaseHostOnly[phaseID]; hostOnly {
				continue
			}
			if phaseCoveredInCorpus(corpus, phaseID) {
				continue
			}
			violations = append(violations, wf.ID+"/"+name+": phase "+phaseID+" not referenced in rendered persona or coordinator prompt corpus")
		}
	}
	contractcheck.FailViolations(t, "workflow phases missing from agent prompt corpus", violations)
}

func buildAgentPromptCorpus(t *testing.T, root string) string {
	t.Helper()
	promptsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts")
	var parts []string

	// Coordinator-facing markdown.
	entries, err := os.ReadDir(promptsDir)
	contractcheck.FailErr(t, "read agents prompts", err)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasPrefix(ent.Name(), "coordinator-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(promptsDir, ent.Name()))
		contractcheck.FailErr(t, "read "+ent.Name(), err)
		parts = append(parts, string(raw))
	}

	// Inform Binding invariants (fires_in) reference phases/scenarios.
	reg := catalogfixture.LoadInformBindings(t)
	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() {
			continue
		}
		parts = append(parts, strings.Join(b.Invariants.FiresIn, "\n"))
		parts = append(parts, string(b.On), b.Render)
	}

	// Live persona renders for bundled worker agents.
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	personaCfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "load persona contract", err)
	for _, agentID := range personaCfg.WorkerAgentIDs() {
		rendered, err := prompts.RenderPersona(context.Background(), engine, agentID, nil)
		contractcheck.FailErr(t, "render persona "+agentID, err)
		parts = append(parts, rendered)
	}

	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func phaseCoveredInCorpus(corpus, phaseID string) bool {
	if strings.Contains(corpus, phaseID) {
		return true
	}
	// Coordinator prompts use the phase ID in their filename.
	needle := "mode-" + phaseID
	return strings.Contains(corpus, needle)
}
