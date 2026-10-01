package hitl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

type identityStore struct {
	hitl.Store
}

func (identityStore) SessionProjectID(context.Context, string) (string, error) {
	return "project", nil
}

type identityAuthzRecorder struct {
	hitl.AuthzRecorder
}

func TestCheckpointRefusesMatchingEmptyActionDigests(t *testing.T) {
	manager := hitl.NewManager(identityStore{}, nil, identityAuthzRecorder{})
	action := hitl.ProposedAction{Tool: "command", Args: map[string]any{"invalid": make(chan int)}}
	response, err := manager.RequestCheckpoint(t.Context(), hitl.CheckpointRequest{
		SessionID: "session", Kind: api.CheckpointKindToolApproval,
		ProposedAction: &action, ApprovalPlan: &hitl.ApprovalPlan{ActionDigest: hitl.GrantKey(action)},
	})
	if err == nil || response != nil || !strings.Contains(err.Error(), "does not match proposed action") {
		t.Fatalf("matching empty identities reached checkpoint storage: response=%+v err=%v", response, err)
	}
}
