package transcript

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

const deniedDirection = "Can we do something better than demo-secret? Even for a demo..."

func toolResultWithDecision(decision *api.CheckpointDecisionMeta) api.Message {
	return api.Message{
		Role:      api.MessageRoleTool,
		Content:   `{"running":true,"handle":"h-1","waited_ms":30000}`,
		Origin:    api.MessageOriginTool,
		Authority: api.ContentAuthorityNone,
		TrustTier: api.ContentTrustTierUntrusted,
		ToolResult: &api.ToolResult{
			Content: `{"running":true,"handle":"h-1","waited_ms":30000}`, Tool: "command",
			ToolCallID: "call_1", CheckpointDecision: decision,
		},
	}
}

func projectOne(t *testing.T, msg api.Message) api.Message {
	t.Helper()
	out := Project([]api.Message{msg})
	for _, projected := range out {
		if projected.Role == api.MessageRoleTool {
			return projected
		}
	}
	t.Fatal("tool message did not survive projection")
	return api.Message{}
}

// Human direction carries instruction authority outside the data-marked tool body.
func TestRejectedCheckpointDirectionReachesTheModelAsInstruction(t *testing.T) {
	projected := projectOne(t, toolResultWithDecision(&api.CheckpointDecisionMeta{
		CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
		Status: api.CheckpointStatusRejected, Tool: "command", Guidance: deniedDirection,
	}))

	if !strings.Contains(projected.Content, deniedDirection) {
		t.Fatalf("direction never reached the wire: %q", projected.Content)
	}
	for _, line := range strings.Split(projected.Content, "\n") {
		if strings.Contains(line, deniedDirection) && strings.HasPrefix(line, "⟦D⟧") {
			t.Fatalf("direction arrived data-marked, so it reads as ignorable: %q", line)
		}
	}
	if !strings.Contains(projected.Content, CheckpointGuidanceNotice()) {
		t.Fatal("direction arrived without the frame that keeps it from reading as permission")
	}
	if !strings.Contains(projected.Content, "⟦D⟧") {
		t.Fatalf("tool body lost its data marking: %q", projected.Content)
	}
}

func TestCheckpointDirectionFollowsItsFrame(t *testing.T) {
	projected := projectOne(t, toolResultWithDecision(&api.CheckpointDecisionMeta{
		CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
		Status: api.CheckpointStatusRejected, Guidance: deniedDirection,
	}))
	frame := strings.Index(projected.Content, CheckpointGuidanceNotice())
	direction := strings.Index(projected.Content, deniedDirection)
	if frame < 0 || direction < 0 || frame > direction {
		t.Fatalf("frame at %d, direction at %d: %q", frame, direction, projected.Content)
	}
}

func TestRejectedCheckpointsWithoutDirectionRetainTheDecision(t *testing.T) {
	for name, decision := range map[string]*api.CheckpointDecisionMeta{
		"rejected without direction": {
			CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
			Status: api.CheckpointStatusRejected,
		},
		"rejected with blank direction": {
			CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
			Status: api.CheckpointStatusRejected, Guidance: "   ",
		},
	} {
		if got := projectOne(t, toolResultWithDecision(decision)).Content; !strings.Contains(got, `"human_checkpoint"`) || !strings.Contains(got, CheckpointRejectionNotice()) || strings.Contains(got, CheckpointGuidanceNotice()) {
			t.Fatalf("%s lost denial or fabricated human guidance: %q", name, got)
		}
	}
}

func TestCheckpointDirectionProjectsOnce(t *testing.T) {
	msg := toolResultWithDecision(&api.CheckpointDecisionMeta{
		CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
		Status: api.CheckpointStatusRejected, Guidance: deniedDirection,
	})
	once := Project([]api.Message{msg})
	twice := Project(once)
	for i := range once {
		if once[i].Content != twice[i].Content {
			t.Fatalf("re-projection changed message %d:\n once %q\ntwice %q",
				i, once[i].Content, twice[i].Content)
		}
	}
	if got := strings.Count(twice[len(twice)-1].Content, deniedDirection); got != 1 {
		t.Fatalf("direction appears %d times after two passes", got)
	}
}

// Checkpoint metadata is authoritative only on the tool result it answers.
func TestCheckpointDirectionOnlyProjectsOntoToolResults(t *testing.T) {
	msg := toolResultWithDecision(&api.CheckpointDecisionMeta{
		CheckpointID: "cp-1", Kind: api.CheckpointKindToolApproval,
		Status: api.CheckpointStatusRejected, Guidance: deniedDirection,
	})
	msg.Role = api.MessageRoleAssistant
	msg.Origin = api.MessageOriginModel
	out := Project([]api.Message{msg})
	for _, projected := range out {
		if strings.Contains(projected.Content, deniedDirection) {
			t.Fatalf("direction projected onto a non-tool row: %q", projected.Content)
		}
	}
}
