package api

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

type backgroundRepo struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	stop    func()
}

func (*backgroundRepo) Brief(context.Context, string) (*repoinfo.Brief, error) {
	return &repoinfo.Brief{}, nil
}
func (*backgroundRepo) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (*backgroundRepo) Warm(string)                                      {}
func (*backgroundRepo) Changed(context.Context, string)                  {}
func (*backgroundRepo) SetOnSettled(func(string))                        {}
func (p *backgroundRepo) Stop() {
	if p.stop != nil {
		p.stop()
	}
}
func (*backgroundRepo) Close() error  { return nil }
func (*backgroundRepo) Epoch() uint64 { return 0 }
func (*backgroundRepo) Busy() bool    { return false }

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
	drained := make(chan error, 1)
	go func() { drained <- s.WaitForBackground(t.Context()) }()
	select {
	case <-p.entered:
	case <-drained:
		t.Fatal("server drain did not join its repository provider")
	case <-t.Context().Done():
		t.Fatal("server drain never reached its repository provider")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WaitForBackground(ctx); err == nil {
		t.Fatal("canceled WaitForBackground returned nil")
	}
	select {
	case <-drained:
		t.Fatal("server drain returned during repository settlement")
	default:
	}
	finish()
	select {
	case err := <-drained:
		testutil.FailErr(t, "server drain after repository settlement", err)
	case <-t.Context().Done():
		t.Fatal("server drain did not finish after repository settlement")
	}
}

func TestBackgroundStopCancelsRepositoryBeforeDrain(t *testing.T) {
	p := &backgroundRepo{entered: make(chan struct{}), release: make(chan struct{})}
	p.stop = sync.OnceFunc(func() { close(p.release) })
	s := newTestServer(t, func(deps *Dependencies) { deps.Source.RepoProvider = p })
	s.StopBackground(t.Context())
	select {
	case <-p.release:
	default:
		t.Fatal("shutdown did not cancel repository analysis before its join")
	}
	testutil.FailErr(t, "wait for background after stop", s.WaitForBackground(t.Context()))
}
