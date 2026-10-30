package repoinfo

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWaitIncludesAnalysisAndSettledCallback(t *testing.T) {
	analyzing, analyzed := make(chan struct{}), make(chan struct{})
	settling, settled := make(chan struct{}), make(chan struct{})
	var callbackFinished atomic.Bool
	p := newProviderWithAnalyze(func(ctx context.Context, _ string) (*Brief, error) {
		close(analyzing)
		select {
		case <-analyzed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Brief{}, nil
	})
	t.Cleanup(func() { _ = p.Close() })
	releaseAnalysis := sync.OnceFunc(func() { close(analyzed) })
	releaseCallback := sync.OnceFunc(func() { close(settled) })
	t.Cleanup(releaseAnalysis)
	t.Cleanup(releaseCallback)
	p.SetOnSettled(func(string) {
		close(settling)
		<-settled
		callbackFinished.Store(true)
	})
	p.Warm(t.TempDir())
	<-analyzing
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait did not join active analysis: %v", err)
	}
	releaseAnalysis()
	<-settling
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait omitted the unsettled callback: %v", err)
	}
	releaseCallback()
	if err := p.Wait(t.Context()); err != nil || !callbackFinished.Load() {
		t.Fatalf("wait returned before callback completion: %v", err)
	}
}

func TestWaitCanReuseIdleIntervals(t *testing.T) {
	p := newProviderWithAnalyze(nil)
	t.Cleanup(func() { _ = p.Close() })
	for range 2 {
		entered, release := make(chan struct{}), make(chan struct{})
		finish := sync.OnceFunc(func() { close(release) })
		t.Cleanup(finish)
		p.analyzeFn = func(ctx context.Context, _ string) (*Brief, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &Brief{}, nil
		}
		p.Warm(t.TempDir())
		<-entered
		var waiters sync.WaitGroup
		for range 8 {
			waiters.Go(func() {
				if err := p.Wait(t.Context()); err != nil {
					t.Errorf("wait for analysis interval: %v", err)
				}
			})
		}
		finish()
		waiters.Wait()
	}
}

func TestStopCancelsAnalysisAndClosesAdmission(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	var starts atomic.Int32
	p := newProviderWithAnalyze(func(ctx context.Context, _ string) (*Brief, error) {
		starts.Add(1)
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	t.Cleanup(func() { _ = p.Close() })
	root := t.TempDir()
	p.Warm(root)
	<-entered
	p.Stop()
	if err := p.Wait(t.Context()); err != nil {
		t.Fatalf("join canceled analysis: %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("analysis was not canceled before join")
	}
	p.Warm(root)
	p.Changed(t.Context(), root)
	if starts.Load() != 1 {
		t.Fatal("stopped provider admitted another analysis")
	}
}

func TestStopPreservesSettledCallbackJoin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := newProviderWithAnalyze(func(context.Context, string) (*Brief, error) { return &Brief{}, nil })
	finish := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { _ = p.Close() })
	t.Cleanup(finish)
	p.SetOnSettled(func(string) { close(entered); <-release })
	p.Warm(t.TempDir())
	<-entered
	p.Stop()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped provider omitted callback join: %v", err)
	}
	finish()
	if err := p.Close(); err != nil {
		t.Fatalf("close stopped provider: %v", err)
	}
}
