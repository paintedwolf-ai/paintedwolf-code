package hostresources

import (
	"strings"
	"testing"
)

func TestProjectAmbientWriteAgentSeesOmitAllowPresence(t *testing.T) {
	docker := State{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: StatusAvailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptOmit,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	plan := ProjectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: []State{docker}},
		Surfaces:        []ExecutionSurface{SurfaceProcessExec},
		MutationCapable: true,
	})
	if len(plan.Resources) != 1 || plan.Resources[0].ID != "docker" {
		t.Fatalf("write agent presence = %#v", plan.Resources)
	}
}

func TestProjectAmbientReadAgentHidesOmitAllow(t *testing.T) {
	docker := State{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: StatusAvailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptOmit,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	plan := ProjectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: []State{docker}},
		Surfaces:        []ExecutionSurface{SurfaceProcessExec},
		MutationCapable: false,
	})
	if len(plan.Resources) != 0 {
		t.Fatalf("read-only omit+allow = %#v", plan.Resources)
	}
}

func TestProjectAmbientWriteAgentSkipsUnsupportedAndUnavailableOmit(t *testing.T) {
	unsupported := State{
		ID: "colima", Label: "Colima", Category: "Containers",
		Status: StatusAvailable, HostSupport: HostUnsupported,
		Access: AccessAllow, Prompt: PromptOmit,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	missing := State{
		ID: "ffmpeg", Label: "FFmpeg", Category: "Media",
		Status: StatusUnavailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptOmit,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	plan := ProjectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: []State{unsupported, missing}},
		Surfaces:        []ExecutionSurface{SurfaceProcessExec},
		MutationCapable: true,
	})
	if len(plan.Resources) != 0 {
		t.Fatalf("non-usable omit rows = %#v", plan.Resources)
	}
}

func TestProjectAmbientGuidanceAndPolicyStillSurface(t *testing.T) {
	advertiseMissing := State{
		ID: "local-db", Label: "Local database", Category: "Data",
		Status: StatusUnavailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptAdvertise,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	avoid := State{
		ID: "aws-cli", Label: "AWS CLI", Category: "Cloud",
		Status: StatusAvailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptAvoid,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	denied := State{
		ID: "sops", Label: "SOPS", Category: "Secrets",
		Status: StatusUnavailable, HostSupport: HostSupported,
		Access: AccessDeny, Prompt: PromptOmit,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	plan := ProjectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: []State{advertiseMissing, avoid, denied}},
		Surfaces:        []ExecutionSurface{SurfaceProcessExec},
		MutationCapable: false,
	})
	if len(plan.Resources) != 3 {
		t.Fatalf("read-only guidance/policy = %#v", plan.Resources)
	}
	if plan.Resources[0].ID != "aws-cli" || plan.Resources[1].ID != "local-db" || plan.Resources[2].ID != "sops" {
		t.Fatalf("category sort = %#v", plan.Resources)
	}
}

func TestProjectAmbientRequiresMatchingSurfaces(t *testing.T) {
	docker := State{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: StatusAvailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptAdvertise,
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
	}
	plan := ProjectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: []State{docker}},
		Surfaces:        nil,
		MutationCapable: true,
	})
	if len(plan.Resources) != 0 {
		t.Fatalf("no process_exec surface = %#v", plan.Resources)
	}
}

func TestProjectAmbientBudgetCountsOmitted(t *testing.T) {
	var resources []State
	for _, id := range []string{"alpha", "beta", "gamma"} {
		resources = append(resources, State{
			ID: id, Label: strings.Repeat(id, 20), Category: "Tools",
			Status: StatusAvailable, HostSupport: HostSupported,
			Access: AccessAllow, Prompt: PromptOmit,
			Surfaces: []ExecutionSurface{SurfaceProcessExec},
		})
	}
	selected := selectAmbient(AmbientInput{
		Snapshot:        Snapshot{Resources: resources},
		Surfaces:        []ExecutionSurface{SurfaceProcessExec},
		MutationCapable: true,
	})
	sortAmbient(selected)
	plan := boundAmbient(selected, ambientLineCost(selected[0])+1)
	if len(plan.Resources) != 1 || plan.Omitted != 2 {
		t.Fatalf("budget plan = %+v", plan)
	}
}

func TestDefinitionPromptFallsBackToOmit(t *testing.T) {
	if got := (Definition{}).Prompt(); got != PromptOmit {
		t.Fatalf("empty prompt = %q", got)
	}
	if got := (Definition{prompt: PromptAvoid}).Prompt(); got != PromptAvoid {
		t.Fatalf("avoid prompt = %q", got)
	}
}
