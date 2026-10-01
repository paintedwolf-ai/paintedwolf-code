package hitl

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type pollStub struct {
	polls int
	resp  *CheckpointResponse
	err   error
}

func (s *pollStub) PollCheckpoint(context.Context, string) (*CheckpointResponse, error) {
	s.polls++
	if s.err != nil {
		return nil, s.err
	}
	if s.polls < 2 {
		return &CheckpointResponse{Status: DecisionStatusPending}, nil
	}
	return s.resp, nil
}

func TestWaitForCheckpointReturnsWhenResolved(t *testing.T) {
	stub := &pollStub{resp: &CheckpointResponse{Status: DecisionStatusApproved}}
	got, err := WaitForCheckpoint(context.Background(), stub, "cp-1")
	testutil.FailErr(t, "WaitForCheckpoint", err)
	if got.Status != DecisionStatusApproved {
		t.Fatalf("status = %s", got.Status)
	}
	if stub.polls < 2 {
		t.Fatalf("polls = %d", stub.polls)
	}
}

func TestWaitForCheckpointHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WaitForCheckpoint(ctx, &pollStub{resp: &CheckpointResponse{Status: DecisionStatusPending}}, "cp-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitForCheckpointReturnsPollError(t *testing.T) {
	want := errors.New("poll failed")
	_, err := WaitForCheckpoint(context.Background(), &pollStub{err: want}, "cp-1")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}
