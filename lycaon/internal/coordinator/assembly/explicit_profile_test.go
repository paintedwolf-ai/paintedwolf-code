package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExplicitToolProfilesDoNotAdvertiseCoordinatorReportProtocol(t *testing.T) {
	for _, profile := range []string{"editor_file", "explore_readonly", "extension_profile"} {
		t.Run(profile, func(t *testing.T) {
			engine := &AssemblyEngine{}
			workflow := &advancingTurnFrameSource{phase: "boot", revision: 1}
			var listed []string
			engine.SetDeps(AssemblyDeps{
				Board: advancingBoard{}, BoardOrientReady: advancingOrientRecorder{source: workflow},
				CoordinatorFrame: workflow,
				Prompts:          prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)}),
				PromptToolLister: func(_ context.Context, _ *api.Session, id string) ([]tools.ToolMeta, error) {
					listed = append(listed, id)
					return []tools.ToolMeta{{Name: "read"}, {Name: "summarize"}}, nil
				},
			})
			sess := &api.Session{ID: "selection", AgentType: "coordinator", Posture: api.SessionPostureBuild}
			frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: profile}}
			history := []api.Message{{Role: api.MessageRoleUser, Content: "Explain this selection."}}
			messages, err := engine.BuildCompletionMessages(t.Context(), sess, history, &frame)
			testutil.FailErr(t, "assemble explicit profile prompt", err)
			if len(listed) != 1 || listed[0] != profile {
				t.Fatalf("tool listing profiles = %v, want only %s", listed, profile)
			}
			if workflow.phase != "work" || frame.Machine.ProfileID != profile {
				t.Fatalf("orientation phase=%s profile=%s, want work with %s", workflow.phase, frame.Machine.ProfileID, profile)
			}
			if messages[len(messages)-1].Content != "board phase=work" {
				t.Fatalf("final system context = %q, want prepared board", messages[len(messages)-1].Content)
			}
			for _, message := range messages {
				if message.Role != api.MessageRoleSystem {
					continue
				}
				if strings.Contains(message.Content, "The host hides it.") || strings.Contains(message.Content, "Surface: investigate.") {
					t.Fatalf("non-report execution was instructed to use coordinator protocol: %s", message.Content)
				}
			}
		})
	}
}
