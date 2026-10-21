package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSurfaceNoteVisibilityTracksOfferedTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, tc := range []struct {
		name       string
		note, wait bool
	}{
		{"no communication tools", false, false},
		{"wait without note", false, true},
		{"note without wait", true, false},
		{"note and wait", true, true},
		{"tools removed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := engine.Render(context.Background(), "agents/coordinator-core.md", map[string]any{
				"profile_has_surface_note": tc.note,
				"profile_has_wait":         tc.wait,
			})
			testutil.FailErr(t, "render communication guidance", err)
			if !strings.Contains(out, "Assistant text alongside tool calls is internal, not shown to the user") {
				t.Fatal("tool text visibility must not depend on communication tool availability")
			}
			if strings.Contains(out, "Use `surface_note` for updates the user should see") != tc.note {
				t.Fatal("visible update guidance must track the offered surface_note tool")
			}
			if strings.Contains(out, "answers to mid-task check-ins") != tc.note {
				t.Fatal("check-in guidance must track the offered surface_note tool")
			}
			if strings.Contains(out, "Before `wait`, send any needed update through `surface_note`") != (tc.note && tc.wait) {
				t.Fatal("wait guidance must require both communication tools")
			}
			if !strings.Contains(out, "A response without tools ends the turn") {
				t.Fatal("visibility guidance must retain the no-tool closeout boundary")
			}
		})
	}
}
