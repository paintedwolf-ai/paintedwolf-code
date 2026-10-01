package tools

// Test fixtures for tool-profile / sandbox / hint-codes inputs.

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fixtureToolProfiles loads bundled tool profiles and path scopes from lycaon/config.
func fixtureToolProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	return profiles
}

// fixtureSandboxConfig returns a permissive sandbox suitable for unit tests.
func fixtureSandboxConfig() sandbox.Config {
	return sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}
}

// fixtureBoundary loads bundled profiles and path scopes for mutation scope tests.
func fixtureBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	b := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	scopes, err := sandbox.LoadPathScopes()
	testutil.FailErr(t, "LoadPathScopes", err)
	b.SetPathScopes(scopes)
	return b
}

func wireFixtureGuidanceRenderer(t *testing.T) {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
}

func moduleHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	wireFixtureGuidanceRenderer(t)
	return cfg
}

func fixtureToolContext(dir string) ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return ToolContext{Roots: roots, ActiveRootID: "r1"}
}
