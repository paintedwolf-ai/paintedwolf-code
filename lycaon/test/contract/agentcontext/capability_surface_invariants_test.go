package contract

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Exclusion disclosures match the resolved capability surface.
func TestI14ExcludedDisclosuresMatchPolicyProjection(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	surfaces := []string{
		surfaceImplementRouting,
		surfaceImplementDispatch,
		surfaceImplementSynthesis,
	}
	for _, rootCount := range []int{0, 1, 2} {
		for _, surfaceID := range surfaces {
			t.Run(fmt.Sprintf("roots=%d/%s", rootCount, surfaceID), func(t *testing.T) {
				t.Parallel()
				allowed := inject.ResolveAgentRoster(surfaceID, spawn.AmbientAllowedAgents(), rootCount, false, true).Effective
				surf, err := capability.Resolve(capability.ResolveInput{
					Profile:        surface.TurnProfile{SurfaceID: surfaceID},
					RootCount:      rootCount,
					SpawnAllowlist: allowed,
					MaxInFlight:    spawn.MaxInFlightTaskWorkers,
				})
				contractcheck.FailErr(t, "Resolve", err)
				block, err := inject.RenderImplementSpawnInject(
					context.Background(), renderer, "sess-inject-test",
					allowed, spawn.MaxInFlightTaskWorkers, surfaceID, rootCount, false, true,
					nil, nil)
				contractcheck.FailErr(t, "RenderImplementSpawnInject", err)
				got := parseExcludedAgentIDs(block)
				want := excludedAgentIDs(surf.ExcludedDisclosures)
				if !excludedIDSetEqual(got, want) {
					t.Fatalf("excluded disclosures got %v want %v\n%s", got, want, block)
				}
			})
		}
	}
}

func TestDisclosurePolicyCatalogCompleteness(t *testing.T) {
	t.Parallel()
	reg, err := capability.LoadDisclosureRegistry()
	contractcheck.FailErr(t, "LoadDisclosureRegistry", err)
	agents, err := agentdef.LoadEffective()
	contractcheck.FailErr(t, "LoadEffective", err)
	for _, agent := range agents {
		id := agent.ID
		if reason, exempt := reg.AgentExempt[id]; exempt {
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("agent exempt %q missing reason", id)
			}
			continue
		}
		if p := capability.AgentDisclosurePolicy(reg, id); p != capability.DisclosureShow && p != capability.DisclosureHide {
			t.Fatalf("agent %q unresolved disclosure policy %q", id, p)
		}
	}
	tools, err := capability.GateableCoordinatorTools()
	contractcheck.FailErr(t, "GateableCoordinatorTools", err)
	for _, name := range tools {
		if reason, exempt := reg.ToolExempt[name]; exempt {
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("tool exempt %q missing reason", name)
			}
			continue
		}
		if p := capability.ToolDisclosurePolicy(reg, name); p != capability.DisclosureShow && p != capability.DisclosureHide {
			t.Fatalf("tool %q unresolved disclosure policy %q", name, p)
		}
	}
}

func TestImplementSpawnExclusionLineIsDerived(t *testing.T) {
	t.Parallel()
	at, err := catalogfixture.StockGuidancePath("implement-spawn")
	contractcheck.FailErr(t, "locate implement-spawn", err)
	raw, err := at.Read()
	contractcheck.FailErr(t, "read implement-spawn", err)
	text := string(raw)
	if strings.Contains(text, "`plan-writer`") || strings.Contains(text, "`plan-reviewer`") {
		t.Fatal("implement-spawn.md must not hand-list excluded agent ids")
	}
	if !strings.Contains(text, "excluded_disclosures") {
		t.Fatal("implement-spawn.md must iterate excluded_disclosures from the capability surface")
	}
}

func parseExcludedAgentIDs(block string) []string {
	idx := strings.Index(block, "**Excluded:**")
	if idx < 0 {
		return nil
	}
	line := block[idx:]
	if nl := strings.Index(line, "\n"); nl >= 0 {
		line = line[:nl]
	}
	re := regexp.MustCompile("`([^`]+)`")
	matches := re.FindAllStringSubmatch(line, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func excludedAgentIDs(views []prompts.SpawnAgentView) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.ID
	}
	sort.Strings(out)
	return out
}

func excludedIDSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
