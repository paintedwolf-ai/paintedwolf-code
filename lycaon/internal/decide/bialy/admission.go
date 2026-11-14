package bialy

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/inference"
)

func (p *process) acquireDevice(ctx context.Context, step string) (*inference.Permit, error) {
	if p.device == nil {
		return nil, nil
	}
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel) //nolint:contextcheck // Either caller cancellation or process exit ends admission.
	defer stop()
	started := time.Now()
	permit, err := p.device.Acquire(waiting, inference.Work{
		Owner: "bialy", Generation: p.generation, Priority: inference.Decision, Step: step,
	})
	log.Debug("decision device admission", "wait_ms", time.Since(started).Milliseconds(), "admitted", err == nil)
	if err != nil && p.lifetime.Err() != nil {
		return nil, errEngineExited
	}
	return permit, err
}

func (p *process) prioritize(ctx context.Context) (func(), error) {
	if p.device == nil {
		return func() {}, nil
	}
	lease, err := p.device.HoldPriority(ctx, inference.Work{
		Owner: "bialy", Generation: p.generation, Priority: inference.Decision,
	})
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// HoldPriority spans several engine calls with one decision-priority lease.
// An engine that cannot start holds nothing; its calls report why.
func (c *Client) HoldPriority(ctx context.Context) func() {
	if !c.Available() {
		return func() {}
	}
	proc, err := c.ensure(ctx)
	if err != nil {
		return func() {}
	}
	release, err := proc.prioritize(ctx)
	if err != nil {
		return func() {}
	}
	return release
}
