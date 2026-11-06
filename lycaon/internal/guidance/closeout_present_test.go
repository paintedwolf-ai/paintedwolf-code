package guidance

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestLatestVisualArtifactIDsSinceUserIntent(t *testing.T) {
	t.Parallel()
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "build a ui"},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Visual: &api.VisualArtifact{ID: "old-art", Mime: "image/png"},
			},
		},
		{Role: api.MessageRoleUser, Content: "show it"},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Visual: &api.VisualArtifact{ID: "art-1", Mime: "image/png"},
			},
		},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Visual: &api.VisualArtifact{ID: "art-2", Mime: "image/jpeg"},
			},
		},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Visual: &api.VisualArtifact{ID: "live-rec", Mime: "video/mp4"},
			},
		},
	}
	got := LatestVisualArtifactIDsSinceUserIntent(history, 3)
	if len(got) != 2 || got[0] != "art-1" || got[1] != "art-2" {
		t.Fatalf("got %#v", got)
	}
}

func TestLatestVisualArtifactIDsIncludesWorkerProofStills(t *testing.T) {
	t.Parallel()
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "build ui"},
		{
			Role: api.MessageRoleTool,
			Content: `<task job_id="j1" state="complete">
<summary>done</summary>
<task_result>ok</task_result>
<proof_json>{"visual_artifact_ids":["worker-art-1","worker-art-2"]}</proof_json>
</task>`,
		},
	}
	got := LatestVisualArtifactIDsSinceUserIntent(history, 3)
	if len(got) != 2 || got[0] != "worker-art-1" || got[1] != "worker-art-2" {
		t.Fatalf("got %#v", got)
	}
}

func TestBindCloseoutPresentArtifactsPrefersReportThenHistory(t *testing.T) {
	t.Parallel()
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "show"},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Visual: &api.VisualArtifact{ID: "from-history", Mime: "image/png"},
			},
		},
	}
	explicit := BindCloseoutPresentArtifacts(
		CoordinatorCompletionReport{Synthesis: "ok", ArtifactIDs: []string{"from-report"}},
		history,
	)
	if len(explicit.ArtifactIDs) != 1 || explicit.ArtifactIDs[0] != "from-report" {
		t.Fatalf("explicit = %#v", explicit.ArtifactIDs)
	}
	fallback := BindCloseoutPresentArtifacts(
		CoordinatorCompletionReport{Synthesis: "ok"},
		history,
	)
	if len(fallback.ArtifactIDs) != 1 || fallback.ArtifactIDs[0] != "from-history" {
		t.Fatalf("fallback = %#v", fallback.ArtifactIDs)
	}
}

// A directive may present what the session has seen: an earlier turn's capture, an
// upload, or an earlier presentation. The default binding stays on this intent.
func TestSessionPresentableArtifactsSpanTheSession(t *testing.T) {
	still := &api.VisualArtifact{ID: "earlier-capture", Mime: "image/png"}
	history := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Look", ContentParts: []api.MessageContentPart{{Content: "image", ArtifactID: "uploaded"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "capture_page", Visual: still}},
		{Role: api.MessageRoleAssistant, Content: "Done", ArtifactIDs: []string{"presented"}},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Show that again"},
	}
	got := SessionPresentableArtifactIDs(history)
	for _, want := range []string{"uploaded", "earlier-capture", "presented"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
	if slices.Contains(got, "never-seen") {
		t.Errorf("unseen id present: %v", got)
	}
	if latest := PresentableVisualArtifactIDs(history); len(latest) != 0 {
		t.Errorf("the default binding reaches past this intent: %v", latest)
	}
}
