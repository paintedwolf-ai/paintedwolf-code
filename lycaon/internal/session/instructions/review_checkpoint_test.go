package instructions

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingCheckpointer struct {
	created []sourceledger.StructuralCheckpointInput
}

func (r *recordingCheckpointer) TurnCheckpoint(context.Context, string, string, int) (sourceledger.Checkpoint, bool, error) {
	return sourceledger.Checkpoint{}, false, nil
}

func (r *recordingCheckpointer) CreateStructuralCheckpoint(_ context.Context, in sourceledger.StructuralCheckpointInput) (sourceledger.Checkpoint, error) {
	r.created = append(r.created, in)
	return sourceledger.Checkpoint{}, nil
}

func TestEmptyPromptCheckpointsOnlyAWorkerRound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input promptinput.Input
		want  int
	}{
		{name: "worker first round", input: promptinput.Input{WorkerJobID: "job-1"}, want: 1},
		{name: "empty prompt", input: promptinput.Input{}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := &recordingCheckpointer{}
			svc := New(stubInstructionStore{session: &api.Session{ID: "sess-1", ProjectID: "proj-1"}}, stubInstructionStore{session: &api.Session{ID: "sess-1", ProjectID: "proj-1"}}, nil, nil,
				func(context.Context, string) error { return nil })
			svc.SetReviewCheckpointer(review)

			appended, err := svc.Apply(context.Background(), "sess-1", tc.input)
			if err != nil || appended != "" {
				t.Fatalf("Apply = %q, %v; want no message", appended, err)
			}
			if len(review.created) != tc.want {
				t.Fatalf("checkpoints = %d, want %d", len(review.created), tc.want)
			}
			if tc.want == 1 {
				got := review.created[0]
				if got.Kind != sourceledger.CheckpointTurn || got.SessionID != "sess-1" || got.ProjectID != "proj-1" || got.Turn != 1 {
					t.Fatalf("checkpoint = %+v", got)
				}
			}
		})
	}
}
