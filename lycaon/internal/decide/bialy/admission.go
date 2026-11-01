package bialy

import (
	"context"
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
	permit, err := p.device.Acquire(waiting, inference.Work{
		Owner: "bialy", Generation: p.generation, Priority: inference.Decision,
	})
	if err != nil && p.lifetime.Err() != nil {
		return nil, errEngineExited
	}
	return permit, err
}
