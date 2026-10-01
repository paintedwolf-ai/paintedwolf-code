package discovery

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoveryRefreshSharesWorkAndKeepsUsableSnapshot(t *testing.T) {
	c := NewCache(nil)
	t.Cleanup(func() { testutil.FailErr(t, "close discovery", c.Close(context.Background())) })
	c.Put("provider", []modelinfo.Entry{{ID: "old"}}, nil)
	c.mu.Lock()
	entry := c.entries["provider"]
	entry.fetched = time.Now().Add(-2 * CacheTTL)
	c.entries["provider"] = entry
	c.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fetch := func(ctx context.Context) ([]modelinfo.Entry, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return []modelinfo.Entry{{ID: "new"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	for range 20 {
		c.Refresh(t.Context(), "provider", fetch)
	}
	<-started
	models, present, fresh, err := c.Snapshot("provider")
	testutil.FailErr(t, "read stale catalog", err)
	if !present || fresh || len(models) != 1 || models[0].ID != "old" {
		t.Fatalf("stale catalog=%+v present=%v fresh=%v", models, present, fresh)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Load(ctx, "provider", fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reader: %v", err)
	}
	close(release)
	models, err = c.Load(t.Context(), "provider", fetch)
	testutil.FailErr(t, "join catalog refresh", err)
	if calls.Load() != 1 || len(models) != 1 || models[0].ID != "new" {
		t.Fatalf("calls=%d catalog=%+v", calls.Load(), models)
	}
}

func TestDiscoveryInvalidationRejectsInFlightResult(t *testing.T) {
	c := NewCache(nil)
	t.Cleanup(func() { testutil.FailErr(t, "close discovery", c.Close(context.Background())) })
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	old := c.start(t.Context(), "provider\x00generation", func(context.Context) ([]modelinfo.Entry, error) {
		close(started)
		<-release
		return []modelinfo.Entry{{ID: "old"}}, nil
	})
	<-started
	c.Invalidate("provider")
	models, err := c.Load(t.Context(), "provider\x00generation", func(context.Context) ([]modelinfo.Entry, error) {
		return []modelinfo.Entry{{ID: "new"}}, nil
	})
	testutil.FailErr(t, "load new generation", err)
	unblock()
	<-old.done
	if len(models) != 1 || models[0].ID != "new" {
		t.Fatal("new request joined invalidated discovery")
	}
	models, fresh, err := c.Get("provider\x00generation")
	testutil.FailErr(t, "read new generation", err)
	if !fresh || len(models) != 1 || models[0].ID != "new" {
		t.Fatal("old discovery overwrote the new generation")
	}
}

func TestDiscoveryFailureRetainsUsableMetadataButFailsFreshRead(t *testing.T) {
	c := NewCache(nil)
	c.Put("provider", []modelinfo.Entry{{ID: "known"}}, nil)
	failed := errors.New("catalog unavailable")
	c.Put("provider", nil, failed)
	models, _, _, err := c.Snapshot("provider")
	if !errors.Is(err, failed) || len(models) != 1 || models[0].ID != "known" {
		t.Fatal("transient refresh failure erased the last usable catalog")
	}
	models, fresh, err := c.Get("provider")
	if !fresh || !errors.Is(err, failed) || len(models) != 0 {
		t.Fatal("fresh read hid discovery failure")
	}
	c.Invalidate("provider")
	if models, present, _, _ := c.Snapshot("provider"); present || len(models) != 0 {
		t.Fatal("explicit invalidation retained old model authority")
	}
}

func TestDiscoveryCloseCancelsAndDrainsRefresh(t *testing.T) {
	c := NewCache(nil)
	started := make(chan struct{})
	c.Refresh(t.Context(), "provider", func(ctx context.Context) ([]modelinfo.Entry, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	<-started
	testutil.FailErr(t, "stop discovery refresh", c.Close(testutil.BoundedContext(t, time.Second)))
	_, err := c.Load(t.Context(), "provider", func(context.Context) ([]modelinfo.Entry, error) {
		t.Error("discovery restarted after shutdown")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("closed discovery: %v", err)
	}
}

func TestDiscoveryCacheReturnsCachedFailure(t *testing.T) {
	cache := NewCache(nil)
	want := errors.New("unauthorized")
	cache.Put("provider", nil, want)

	models, ok, gotErr := cache.Get("provider")
	if !ok || !errors.Is(gotErr, want) || len(models) != 0 {
		t.Fatalf("get = (%v, %v, %v), want cached failure", models, gotErr, ok)
	}
}
