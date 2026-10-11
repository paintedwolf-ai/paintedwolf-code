package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type reviewCheckpointRecorder struct {
	sourceledger.Recorder
	inputs []sourceledger.StructuralCheckpointInput
}

func (r *reviewCheckpointRecorder) CreateStructuralCheckpoint(
	_ context.Context,
	in sourceledger.StructuralCheckpointInput,
) (sourceledger.Checkpoint, error) {
	r.inputs = append(r.inputs, in)
	return sourceledger.Checkpoint{
		ProjectID: in.ProjectID, Kind: in.Kind, SessionID: in.SessionID, Turn: in.Turn,
	}, nil
}

func TestReviewCheckpointOnlyOpensForRealUserIntent(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	recorder := &reviewCheckpointRecorder{}
	mgr.SetSourceLedger(recorder, tools.SourceHistory{}, nil, nil, recorder, nil)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	message := func(id string, origin api.MessageOrigin, visibility api.MessageVisibility, kind api.MessageKind) api.Message {
		return api.Message{
			ID: id, Role: api.MessageRoleUser, Content: id, Origin: origin,
			Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
			Visibility: visibility, Kind: kind, CreatedAt: time.Now().UTC(),
		}
	}
	first := message("user-1", api.MessageOriginUser, api.MessageVisibilityTranscript, "")
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, first)
	testutil.FailErr(t, "append first user turn", sessions.AppendMessages(ctx, sess.ID, first))

	hostKick := message("host-kick", api.MessageOriginHost, api.MessageVisibilityInternal, api.MessageKindHostKick)
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, hostKick)
	testutil.FailErr(t, "append host kick", sessions.AppendMessages(ctx, sess.ID, hostKick))

	second := message("user-2", api.MessageOriginUser, api.MessageVisibilityTranscript, "")
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, second)
	if len(recorder.inputs) != 2 {
		t.Fatalf("review checkpoints = %d, want one for each of two user turns", len(recorder.inputs))
	}
	if recorder.inputs[0].Turn != 1 || recorder.inputs[1].Turn != 2 {
		t.Fatalf("review checkpoint turns = %d, %d, want 1, 2",
			recorder.inputs[0].Turn, recorder.inputs[1].Turn)
	}
}

func (r *reviewCheckpointRecorder) TurnCheckpoint(context.Context, string, string, int) (sourceledger.Checkpoint, bool, error) {
	return sourceledger.Checkpoint{}, false, nil
}
