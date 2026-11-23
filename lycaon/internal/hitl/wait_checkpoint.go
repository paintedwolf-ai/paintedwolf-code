package hitl

import (
	"context"
	"time"
)

const checkpointPollInterval = 200 * time.Millisecond

type checkpointWaitNotificationKey struct{}

// WithCheckpointWaitNotification observes the checkpoint a caller waits on.
func WithCheckpointWaitNotification(ctx context.Context, notify func(context.Context, string)) context.Context {
	return context.WithValue(ctx, checkpointWaitNotificationKey{}, notify)
}

type checkpointPoller interface {
	PollCheckpoint(ctx context.Context, checkpointID string) (*CheckpointResponse, error)
}

// checkpointWaitObserver brackets the time an execution blocks on a person.
type checkpointWaitObserver interface {
	ObserveCheckpointWait(ctx context.Context, checkpointID string) (end func())
}

// WaitForCheckpoint polls until the checkpoint leaves pending, on WaitContext.
// A poller that observes waits sees the whole blocked interval.
func WaitForCheckpoint(ctx context.Context, poller checkpointPoller, checkpointID string) (*CheckpointResponse, error) {
	defer beginHumanWait(ctx)()
	if notify, _ := ctx.Value(checkpointWaitNotificationKey{}).(func(context.Context, string)); notify != nil {
		notify(ctx, checkpointID)
	}
	if checkpoints, ok := poller.(*Checkpoints); ok {
		defer checkpoints.Sessions.ObserveCheckpointWait(ctx, checkpointID)()
	} else if observer, ok := poller.(checkpointWaitObserver); ok {
		defer observer.ObserveCheckpointWait(ctx, checkpointID)()
	}
	ctx = WaitContext(ctx)
	ticker := time.NewTicker(checkpointPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			resp, err := poller.PollCheckpoint(ctx, checkpointID)
			if err != nil {
				return nil, err
			}
			if resp.Status != DecisionStatusPending {
				return resp, nil
			}
		}
	}
}
