package guidance

import (
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

func TestParseCoordinatorCompletionReportArtifactIDs(t *testing.T) {
	t.Parallel()
	envelope := `{"synthesis":"Report.","cited_evidence":[],"artifact_ids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}`
	report, ok := ParseCoordinatorCompletionReport(envelope)
	if !ok {
		t.Fatal("expected parse")
	}
	if len(report.ArtifactIDs) != 1 || report.ArtifactIDs[0] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("artifact_ids = %#v", report.ArtifactIDs)
	}
}
