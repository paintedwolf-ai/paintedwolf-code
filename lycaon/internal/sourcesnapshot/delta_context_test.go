package sourcesnapshot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

type joiningContext struct {
	context.Context
	once   sync.Once
	joined chan struct{}
}

func (c *joiningContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

func TestSharedSnapshotSurvivesItsFirstConsumersCancellation(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	req := Request{Roots: []Root{{Path: root}}}
	started, release := make(chan struct{}), make(chan struct{})
	releaseSurvey := sync.OnceFunc(func() { close(release) })
	defer releaseSurvey()
	var first sync.Once
	store.onSurvey = func(string) { first.Do(func() { close(started); <-release }) }
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	ownerDone := make(chan error, 1)
	go func() { _, err := store.Ensure(ownerCtx, req); ownerDone <- err }()
	select {
	case <-started:
	case err := <-ownerDone:
		t.Fatalf("initial capture ended before survey: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("initial capture did not begin surveying")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	joiner := &joiningContext{Context: ctx, joined: make(chan struct{})}
	otherDone := make(chan error, 1)
	go func() {
		snapshot, err := store.Ensure(joiner, req)
		if err == nil && snapshot.FileCount != 1 {
			err = errors.New("shared snapshot lost its file")
		}
		otherDone <- err
	}()
	<-joiner.joined
	cancelOwner()
	releaseSurvey()
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first consumer = %v, want cancellation", err)
	}
	testutil.FailErr(t, "remaining consumer publishes", <-otherDone)
}

type observingScopes struct{ observed context.Context }

func (s *observingScopes) Capture(ctx context.Context, root string) *sourcescope.Scope {
	s.observed = ctx
	return sourcescope.New(root, sourcescope.Options{})
}

func TestDeltaScopeUsesEventContext(t *testing.T) {
	store := openSnapshotStore(t)
	provider := &observingScopes{}
	store.SetScopes(provider)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store.deltas.observe(ctx, repochange.Event{
		Kind: repochange.WorktreeChanged, ProjectDir: t.TempDir(), Paths: []string{"main.go"},
	})
	if provider.observed != ctx {
		t.Fatalf("scope provider lost event context: %v", provider.observed)
	}
}

func TestCanceledDeltaAdmissionRequiresResurvey(t *testing.T) {
	for _, timing := range []string{"before admission", "during admission", "not canceled"} {
		t.Run(timing, func(t *testing.T) {
			root := t.TempDir()
			coverRoot(t, root)
			tracker := newDeltaTracker()
			tracker.coveredBy(root, "previous", "scope", tracker.sequence(), true)
			prior := tracker.begin(root, "scope")
			if prior.covered != "previous" {
				t.Fatal("fixture cannot reuse its covered snapshot")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if timing == "before admission" {
				cancel()
			}
			tracker.admit = func(eventCtx context.Context, _, _ string) bool {
				if eventCtx != ctx {
					t.Fatal("admission lost event context")
				}
				if timing == "during admission" {
					cancel()
				}
				return false
			}
			tracker.observe(ctx, repochange.Event{
				Kind: repochange.WorktreeChanged, ProjectDir: root, Paths: []string{"main.go"},
			})
			// A publication already in progress must not erase the resurvey request.
			tracker.coveredBy(root, "concurrent", "scope", prior.upTo, true)
			next := tracker.begin(root, "scope")
			if timing == "not canceled" {
				if next.covered != "concurrent" {
					t.Fatal("healthy excluded event invalidated covered history")
				}
			} else if next.covered != "" {
				t.Fatalf("canceled admission reused stale snapshot %q", next.covered)
			}
		})
	}
}
