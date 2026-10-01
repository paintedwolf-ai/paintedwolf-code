package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApprovedCheckpointReachesModelAsData(t *testing.T) {
	decision := api.CheckpointDecisionMeta{
		CheckpointID: "cp-approved", Kind: api.CheckpointKindToolApproval,
		Status: api.CheckpointStatusApproved, Tool: "command", Subject: "local network",
		Guidance: "not direction on an approval",
	}
	msg := toolResultWithDecision(&decision)
	parts := checkpointDecisionParts(msg)
	if len(parts) != 1 || parts[0].Authority != api.ContentAuthorityNone || parts[0].Origin != api.MessageOriginHost {
		t.Fatalf("approval must be host data, not permission: %+v", parts)
	}
	var receipt struct {
		Decision api.CheckpointDecisionMeta `json:"human_checkpoint"`
	}
	testutil.FailErr(t, "decode approval receipt", json.Unmarshal([]byte(parts[0].Content), &receipt))
	if receipt.Decision.CheckpointID != decision.CheckpointID || receipt.Decision.Status != decision.Status || receipt.Decision.Guidance != "" {
		t.Fatalf("wrong approval receipt: %+v", receipt)
	}
	projected := projectOne(t, msg)
	if !strings.Contains(projected.Content, "⟦D⟧"+parts[0].Content) {
		t.Fatalf("approval receipt missing from data-marked wire: %q", projected.Content)
	}
	if strings.Contains(projected.Content, decision.Guidance) || msg.ToolResult.CheckpointDecision.Guidance != decision.Guidance {
		t.Fatal("approval leaked direction or mutated the durable decision")
	}
	if twice := projectOne(t, projected); twice.Content != projected.Content {
		t.Fatal("approval duplicated on repeated projection")
	}

}

func TestApprovalMetadataCannotProjectFromAssistantText(t *testing.T) {
	msg := toolResultWithDecision(&api.CheckpointDecisionMeta{
		CheckpointID: "cp-approved", Status: api.CheckpointStatusApproved,
	})
	msg.Role = api.MessageRoleAssistant
	msg.Origin = api.MessageOriginModel
	if parts := checkpointDecisionParts(msg); len(parts) != 0 {
		t.Fatal("non-tool message projected approval authority")
	}
}
