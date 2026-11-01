package bialy

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/inference"
)

func (p *process) acquireDevice(ctx context.Context) (*inference.Permit, error) {
	if p.device == nil {
		return nil, nil
	}
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()
	started := time.Now()
	permit, err := p.device.Acquire(waiting, inference.Work{
		Owner: "bialy", Generation: p.generation, Priority: inference.Decision,
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
