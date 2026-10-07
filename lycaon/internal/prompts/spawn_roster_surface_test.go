package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
)

func excludedDisclosuresForTest(t *testing.T, data prompts.SpawnRosterData) []prompts.SpawnAgentView {
	t.Helper()
	reg, err := capability.LoadDisclosureRegistry()
	if err != nil {
		t.Fatalf("LoadDisclosureRegistry: %v", err)
	}
	return capability.FilterAgentsForDisclosure(data.NotSpawnableAgents, reg)
}

func spawnRosterTemplateVarsForTest(t *testing.T, data prompts.SpawnRosterData) map[string]any {
	t.Helper()
	return prompts.SpawnRosterTemplateVars(data, excludedDisclosuresForTest(t, data))
}

func TestSpawnRosterRoleVarsSplitByEditAndSkipCoordinator(t *testing.T) {
	data := prompts.SpawnRosterData{SpawnAgents: []prompts.SpawnAgentView{
		{ID: "web-researcher"},
		{ID: "implementer", CanEdit: true},
		{ID: "coordinator", CanEdit: true, SurfaceVariable: true},
		{ID: "path-explorer"},
	}}
	vars := prompts.SpawnRosterRoleVars(data)
	if got := strings.Join(vars["spawn_read_agent_ids"].([]string), ","); got != "path-explorer,web-researcher" {
		t.Fatalf("spawn_read_agent_ids = %q", got)
	}
	if got := strings.Join(vars["spawn_write_agent_ids"].([]string), ","); got != "implementer" {
		t.Fatalf("spawn_write_agent_ids = %q", got)
	}
}

func TestLoadSpawnRosterSurfaceImplementDefault(t *testing.T) {
	data, err := prompts.LoadSpawnRosterSurface(spawn.AmbientAllowedAgents(), spawn.MaxInFlightTaskWorkers, nil, nil, nil)
	if err != nil {
		t.Fatalf("LoadSpawnRosterSurface: %v", err)
	}
	if len(data.SpawnAgents) != len(spawn.AmbientAllowedAgents()) {
		t.Fatalf("spawn agents = %d want %d", len(data.SpawnAgents), len(spawn.AmbientAllowedAgents()))
	}
	if data.MaxInFlight != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("max_in_flight = %d want %d", data.MaxInFlight, spawn.MaxInFlightTaskWorkers)
	}

	var explorer *prompts.SpawnAgentView
	var implementer *prompts.SpawnAgentView
	for i := range data.SpawnAgents {
		switch data.SpawnAgents[i].ID {
		case "path-explorer":
			explorer = &data.SpawnAgents[i]
		case "implementer":
			implementer = &data.SpawnAgents[i]
		}
	}
	if explorer == nil || implementer == nil {
		t.Fatal("missing expected spawn agents")
	}
	if explorer.CanEdit {
		t.Fatal("path-explorer should not can_edit")
	}
	if !implementer.CanEdit {
		t.Fatal("implementer should can_edit")
	}
	if !implementer.CanCommand {
		t.Fatal("implementer must expose command")
	}
	if explorer.CanCommand {
		t.Fatal("path-explorer must not expose command on explore_readonly")
	}
	for _, tool := range explorer.EnabledTools {
		if tool == "command" {
			t.Fatalf("path-explorer enabled tools must not include command: %v", explorer.EnabledTools)
		}
	}

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	spawnVars := spawnRosterTemplateVarsForTest(t, data)
	if err := prompts.MergeCoordinatorKickPolicyVars(spawnVars); err != nil {
		t.Fatalf("MergeCoordinatorKickPolicyVars: %v", err)
	}
	spawnOut, err := engine.Render(context.Background(), "inject/implement-spawn.md", spawnVars)
	if err != nil {
		t.Fatalf("Render implement-spawn: %v", err)
	}
	for _, want := range []string{"implementer"} {
		if !strings.Contains(spawnOut, want) {
			t.Fatalf("implement-spawn missing %q", want)
		}
	}

	allowed := map[string]bool{}
	for _, id := range spawn.AmbientAllowedAgents() {
		allowed[id] = true
	}
	for _, a := range data.NotSpawnableAgents {
		if allowed[a.ID] {
			t.Fatalf("not spawnable includes allowed agent %q", a.ID)
		}
	}
	foundPlanWriter := false
	for _, a := range data.NotSpawnableAgents {
		if a.ID == "plan-writer" {
			foundPlanWriter = true
		}
	}
	if !foundPlanWriter {
		t.Fatal("plan-writer should be not spawnable")
	}
}

func TestSpawnRosterCoordinatorSurfaceVariable(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})

	cases := []struct {
		name        string
		tools       []string
		wantRender  []string
		denyRender  []string
		wantEdit    bool
		wantCommand bool
	}{
		{
			name:        "investigate grants inline edits and command",
			tools:       []string{"read", "write", "edit", "command", "task", "update_progress"},
			wantRender:  []string{"edits inline; runs commands on this surface"},
			denyRender:  []string{"(no command)", "routes product writes via workers"},
			wantEdit:    true,
			wantCommand: true,
		},
		{
			name:        "orchestrate routes writes via workers, no command",
			tools:       []string{"task", "worker_cancel", "pack_board", "wait"},
			wantRender:  []string{"routes product writes via workers on this surface"},
			denyRender:  []string{"(no command)", "edits inline", "runs commands"},
			wantEdit:    false,
			wantCommand: false,
		},
		{
			name:        "overlay-promote routes writes but runs commands",
			tools:       []string{"promote_overlay", "task", "read", "command", "wait"},
			wantRender:  []string{"routes product writes via workers; runs commands on this surface"},
			denyRender:  []string{"(no command)", "edits inline"},
			wantEdit:    false,
			wantCommand: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			surfaceTools := map[string][]string{"coordinator": tc.tools}
			data, err := prompts.LoadSpawnRosterSurface([]string{"coordinator"}, 3, surfaceTools, nil, nil)
			if err != nil {
				t.Fatalf("LoadSpawnRosterSurface: %v", err)
			}
			if len(data.SpawnAgents) != 1 {
				t.Fatalf("spawn agents = %d want 1", len(data.SpawnAgents))
			}
			coord := data.SpawnAgents[0]
			if !coord.SurfaceVariable {
				t.Fatal("coordinator row must be surface_variable")
			}
			if coord.CanEdit != tc.wantEdit {
				t.Fatalf("can_edit = %t want %t", coord.CanEdit, tc.wantEdit)
			}
			if coord.CanCommand != tc.wantCommand {
				t.Fatalf("can_command = %t want %t", coord.CanCommand, tc.wantCommand)
			}

			vars := spawnRosterTemplateVarsForTest(t, data)
			if err := prompts.MergeCoordinatorKickPolicyVars(vars); err != nil {
				t.Fatalf("MergeCoordinatorKickPolicyVars: %v", err)
			}
			out, err := engine.Render(context.Background(), "inject/implement-spawn.md", vars)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, want := range tc.wantRender {
				if !strings.Contains(out, want) {
					t.Fatalf("render missing %q:\n%s", want, out)
				}
			}
			for _, deny := range tc.denyRender {
				if strings.Contains(out, deny) {
					t.Fatalf("render should not contain %q:\n%s", deny, out)
				}
			}
		})
	}
}

func TestLoadSpawnRosterSurfaceUnknownAgent(t *testing.T) {
	_, err := prompts.LoadSpawnRosterSurface([]string{"not-a-real-agent"}, 3, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSpawnRosterSurfaceNoAllowedAgents(t *testing.T) {
	data, err := prompts.LoadSpawnRosterSurface(nil, 3, nil, nil, nil)
	if err != nil {
		t.Fatalf("LoadSpawnRosterSurface: %v", err)
	}
	if len(data.SpawnAgents) != 0 {
		t.Fatalf("expected an empty roster, got %d agents", len(data.SpawnAgents))
	}
}

func TestSpawnRosterTemplateVars(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := prompts.SpawnRosterTemplateVars(prompts.SpawnRosterData{
		MaxInFlight: 3,
		SpawnAgents: []prompts.SpawnAgentView{{
			ID:           "path-explorer",
			Name:         "Path Explorer",
			Description:  "Bounded survey",
			ToolProfile:  "explore_readonly",
			EnabledTools: []string{"grep", "read", "find"},
			CanCommand:   false,
			ReadsProject: true,
		}, {
			ID:           "web-researcher",
			Description:  "External sources",
			ToolProfile:  "web_research",
			EnabledTools: []string{"fetch_url", "web_search"},
		}},
	}, nil)
	if err := prompts.MergeCoordinatorKickPolicyVars(vars); err != nil {
		t.Fatalf("MergeCoordinatorKickPolicyVars: %v", err)
	}
	out, err := engine.Render(context.Background(), "inject/implement-spawn.md", vars)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"path-explorer", "Bounded survey", "(no command)", "(no command; cannot read project files)", "in-flight"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out missing %q: %s", want, out)
		}
	}
	if !strings.Contains(out, "**3**") {
		t.Fatalf("out missing concurrency cap: %s", out)
	}
}
