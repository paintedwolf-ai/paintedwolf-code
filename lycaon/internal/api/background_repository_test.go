package api

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repoinfo"
)

type backgroundRepo struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*backgroundRepo) Brief(context.Context, string) (*repoinfo.Brief, error) {
	return &repoinfo.Brief{}, nil
}
func (*backgroundRepo) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (*backgroundRepo) Warm(string)                                      {}
func (*backgroundRepo) Changed(context.Context, string)                  {}
func (*backgroundRepo) SetOnSettled(func(string))                        {}
func (*backgroundRepo) Close() error                                     { return nil }

func (p *backgroundRepo) Wait(ctx context.Context) error {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBackgroundDrainIncludesRepositorySettlement(t *testing.T) {
	p := &backgroundRepo{entered: make(chan struct{}), release: make(chan struct{})}
	finish := sync.OnceFunc(func() { close(p.release) })
	s := newTestServer(t, func(deps *Dependencies) { deps.Source.RepoProvider = p })
	t.Cleanup(finish)
	drained := make(chan struct{})
	go func() { s.WaitForBackground(t.Context()); close(drained) }()
	select {
	case <-p.entered:
	case <-drained:
		t.Fatal("server drain did not join its repository provider")
	case <-t.Context().Done():
		t.Fatal("server drain never reached its repository provider")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.WaitForBackground(ctx)
	select {
	case <-drained:
		t.Fatal("server drain returned during repository settlement")
	default:
	}
	finish()
	select {
	case <-drained:
	case <-t.Context().Done():
		t.Fatal("server drain did not finish after repository settlement")
	}
}
