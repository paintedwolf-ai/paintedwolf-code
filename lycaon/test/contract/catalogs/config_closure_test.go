package contract

import (
	"context"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowManifestAllowedAgentsExist(t *testing.T) {
	t.Parallel()
	catalog := workflowfixture.ContractAllResolvedManifests(t)
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	for key, m := range catalog {
		for _, agentID := range m.AllowedAgents {
			if _, err := reg.Get(agentID); err != nil {
				t.Errorf("%s: agent %q not in stock agents: %v", key, agentID, err)
			}
		}
	}
}

func TestWorkflowManifestRulesPathsExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfgRoot := filepath.Join(root, "lycaon", "config")
	catalog := workflowfixture.ContractAllResolvedManifests(t)
	for key, m := range catalog {
		for _, rulePath := range m.Rules {
			rulePath = strings.TrimSpace(rulePath)
			if rulePath == "" {
				continue
			}
			rel := strings.TrimPrefix(rulePath, "config/")
			full := filepath.Join(cfgRoot, rel)
			if _, err := os.Stat(full); err != nil {
				t.Errorf("%s: rules path %q: %v", key, rulePath, err)
			}
		}
	}
}

func TestPostureRegistryRulesPathsExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfgRoot := filepath.Join(root, "lycaon", "config")
	reg, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)
	for _, posture := range sessionposture.AllSessionPostures() {
		spec, err := reg.Get(posture)
		contractcheck.FailErr(t, "reg.Get failed", err)
		for _, rulePath := range spec.Rules {
			rel := strings.TrimPrefix(strings.TrimSpace(rulePath), "config/")
			full := filepath.Join(cfgRoot, rel)
			if _, err := os.Stat(full); err != nil {
				t.Errorf("posture %q rules path %q: %v", posture, rulePath, err)
			}
		}
	}
}

func TestBundledToolProfilesLoad(t *testing.T) {
	t.Parallel()
	_, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
}
