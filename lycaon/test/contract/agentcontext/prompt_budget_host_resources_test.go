package contract

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/testutil"
)

// promptBudgetHostResourceVars projects the bundled catalog through each
// platform's realizations as available and host-supported, the write-agent
// worst case, and keeps the widest projection for role.
func promptBudgetHostResourceVars(t *testing.T, role hostresources.AgentRole) map[string]any {
	t.Helper()
	raw, err := config.Read(config.HostResources)
	testutil.FailErr(t, "read bundled host resources", err)
	catalog, err := hostresources.ParseCatalog(raw, "built-in")
	testutil.FailErr(t, "parse bundled host resources", err)
	var widest hostresources.AmbientPlan
	var widestUnconfined bool
	for _, platform := range []string{"macos", "linux", "windows"} {
		plan := promptBudgetAmbientPlan(catalog, platform, role)
		if promptBudgetRosterBytes(plan) > promptBudgetRosterBytes(widest) {
			widest, widestUnconfined = plan, platform == "windows"
		}
	}
	surface := prompts.AgentPromptSurface{
		HostResources:        profiles.HostResourceViews(widest),
		HostResourcesOmitted: widest.Omitted,
		HostUnconfined:       widestUnconfined,
	}
	return prompts.AgentPromptSurfaceTemplateVars(surface)
}

// promptBudgetRealization is def's realization on platform, as discovery selects it.
func promptBudgetRealization(def hostresources.Definition, platform string) *hostresources.Realization {
	for i := range def.Realizations {
		if len(def.Realizations[i].Platforms) == 0 || slices.Contains(def.Realizations[i].Platforms, platform) {
			return &def.Realizations[i]
		}
	}
	return nil
}

// promptBudgetRosterBytes sizes a plan's prompt rows to pick the widest platform.
func promptBudgetRosterBytes(plan hostresources.AmbientPlan) int {
	raw, _ := json.Marshal(profiles.HostResourceViews(plan))
	return len(raw)
}

func promptBudgetAmbientPlan(catalog hostresources.Catalog, platform string, role hostresources.AgentRole) hostresources.AmbientPlan {
	resources := make([]hostresources.State, 0, len(catalog.Resources))
	for _, def := range catalog.Resources {
		realization := promptBudgetRealization(def, platform)
		if realization == nil {
			continue
		}
		resources = append(resources, hostresources.State{
			ID:          def.ID,
			Label:       def.Label,
			Category:    def.Category,
			Status:      hostresources.StatusAvailable,
			HostSupport: hostresources.HostSupported,
			Access:      hostresources.AccessAllow,
			Prompt:      def.Prompt(),
			Surfaces:    append([]hostresources.ExecutionSurface(nil), def.Surfaces...),
			Uses:        realization.Uses,
		})
	}
	return hostresources.ProjectAmbient(hostresources.AmbientInput{
		Snapshot: hostresources.Snapshot{Resources: resources},
		Reach: hostresources.Reach{
			Surfaces:   []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec},
			Role:       role,
			Offered:    hostresources.OfferedCapabilities([]string{"command", "terminal_open"}),
			Unconfined: platform == "windows",
		},
		MutationCapable: true,
	})
}

// promptBudgetRole is the host-resource role an agent template renders for.
func promptBudgetRole(agentID string) hostresources.AgentRole {
	if agentID == orchestration.ProfileCoordinator {
		return hostresources.RoleCoordinator
	}
	return hostresources.RoleWorker
}

// writeAgentForBudget reports ToolProfileMutationCapable for agentID's tool profile.
func writeAgentForBudget(t *testing.T, agentID string) bool {
	t.Helper()
	profileID, err := prompts.ToolProfileForAgent(agentID)
	if err != nil {
		profileID = agentID
	}
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	for _, profile := range profiles {
		if profile.ID == profileID {
			return prompts.ToolProfileMutationCapable(profile)
		}
	}
	return false
}

func mergePromptBudgetHostResources(t *testing.T, vars map[string]any, role hostresources.AgentRole) {
	t.Helper()
	if vars == nil {
		return
	}
	roster := promptBudgetHostResourceVars(t, role)
	if groups, ok := roster["agent_host_resources"].([]map[string]any); ok && len(groups) > 0 {
		vars["agent_host_resources"] = groups
	}
	if omitted, ok := roster["agent_host_resources_omitted"].(int); ok && omitted > 0 {
		vars["agent_host_resources_omitted"] = omitted
	}
	vars["agent_host_resource_uses"] = roster["agent_host_resource_uses"]
	vars["host_unconfined"] = roster["host_unconfined"]
}

func requirePromptBudgetHostResources(t *testing.T, vars map[string]any, what string) {
	t.Helper()
	list, ok := vars["agent_host_resources"].([]map[string]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s measured without the bundled host-resource inventory; caps would understate the shipped prompt", what)
	}
}

func TestWriteAgentForBudgetMatchesMutationCapable(t *testing.T) {
	t.Parallel()
	if !writeAgentForBudget(t, "implementer") {
		t.Fatal("implementer must be a write agent")
	}
	if !writeAgentForBudget(t, "coordinator") {
		t.Fatal("coordinator must be a write agent")
	}
	if writeAgentForBudget(t, "repo-researcher") {
		t.Fatal("repo-researcher must not be a write agent")
	}
	if writeAgentForBudget(t, "skeptic") {
		t.Fatal("skeptic must not be a write agent")
	}
}

func TestPromptBudgetMeasuresTheShippedHostResourceInventory(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	mergePromptBudgetHostResources(t, vars, hostresources.RoleCoordinator)
	requirePromptBudgetHostResources(t, vars, "bundled host-resource inventory")
	groups := vars["agent_host_resources"].([]map[string]any)
	n := 0
	for _, group := range groups {
		if cat, _ := group["category"].(string); strings.TrimSpace(cat) == "" {
			t.Fatalf("inventory group without a category: %#v", group)
		}
		rows, _ := group["resources"].([]map[string]any)
		if len(rows) == 0 {
			t.Fatalf("empty inventory group: %#v", group)
		}
		for _, row := range rows {
			if id, _ := row["id"].(string); strings.TrimSpace(id) == "" {
				t.Fatalf("inventory entry without an id: %#v", row)
			}
			if label, _ := row["label"].(string); strings.TrimSpace(label) == "" {
				t.Fatalf("inventory entry without a label: %#v", row)
			}
		}
		n += len(rows)
	}
	if n < 20 {
		t.Fatalf("bundled inventory shrank to %d entries; the budget would understate write-agent prompts", n)
	}
}
