package resourcelifecycle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryReleaseAndDispose(t *testing.T) {
	registry := New()
	scope := SessionScope("s1")
	var calls []string
	failKind := true
	if err := registry.Register(ScopeSession, "kind", 20, func(context.Context, Scope) error {
		calls = append(calls, "kind")
		if failKind {
			return errors.New("kind failed")
		}
		return nil
	}); err != nil {
		testutil.FailErr(t, "register kind cleanup", err)
	}
	if err := registry.Track(scope, "tracked", 10, func(context.Context, Scope) error {
		calls = append(calls, "tracked")
		return nil
	}); err != nil {
		testutil.FailErr(t, "track resource", err)
	}
	if err := registry.Release(t.Context(), scope); err == nil {
		t.Fatal("release should join cleanup failures")
	}
	if !reflect.DeepEqual(calls, []string{"tracked", "kind"}) {
		t.Fatalf("calls = %v", calls)
	}
	failKind = false
	calls = nil
	_ = registry.Dispose(t.Context(), scope)
	_ = registry.Dispose(t.Context(), scope)
	if !reflect.DeepEqual(calls, []string{"kind"}) {
		t.Fatalf("terminal calls = %v", calls)
	}
	if err := registry.Track(scope, "late", 1, func(context.Context, Scope) error { return nil }); err == nil {
		t.Fatal("disposed scope accepted a resource")
	}
}

func TestRegistrySerializesOneScope(t *testing.T) {
	registry := New()
	scope := SessionScope("s1")
	started := make(chan struct{}, 1)
	resume := make(chan struct{})
	var mu sync.Mutex
	active := 0
	maxActive := 0
	testutil.FailErr(t, "register cleanup", registry.Register(ScopeSession, "session", 1, func(context.Context, Scope) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		started <- struct{}{}
		<-resume
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}))
	done := make(chan error, 2)
	go func() { done <- registry.Release(t.Context(), scope) }()
	<-started
	go func() { done <- registry.Release(t.Context(), scope) }()
	close(resume)
	testutil.FailErr(t, "first release", <-done)
	testutil.FailErr(t, "second release", <-done)
	if maxActive != 1 {
		t.Fatalf("max active cleanup = %d", maxActive)
	}
}

func TestRegistryRetriesFailedTrackedCleanup(t *testing.T) {
	registry := New()
	scope := SessionScope("s1")
	attempts := 0
	testutil.FailErr(t, "track resource", registry.Track(scope, "resource", 1, func(context.Context, Scope) error {
		attempts++
		if attempts == 1 {
			return errors.New("close failed")
		}
		return nil
	}))
	if err := registry.Dispose(t.Context(), scope); err == nil {
		t.Fatal("dispose succeeded on failed cleanup")
	}
	testutil.FailErr(t, "retry dispose", registry.Dispose(t.Context(), scope))
	if attempts != 2 {
		t.Fatalf("cleanup attempts = %d", attempts)
	}
	if err := registry.Track(scope, "late", 1, func(context.Context, Scope) error { return nil }); err == nil {
		t.Fatal("disposed scope accepted a resource")
	}
}

func TestRegistryForgetsTerminalScopeState(t *testing.T) {
	registry := New()
	scope := SessionScope("deleted")
	testutil.FailErr(t, "dispose", registry.Dispose(t.Context(), scope))
	if len(registry.scopes) != 1 {
		t.Fatalf("retained scopes = %d, want terminal tombstone", len(registry.scopes))
	}
	testutil.FailErr(t, "forget disposed", registry.ForgetDisposed(scope))
	if len(registry.scopes) != 0 {
		t.Fatalf("retained scopes = %d after durable deletion", len(registry.scopes))
	}
}

func TestDisposalRulesRunOnlyWhenTheScopeIsDisposed(t *testing.T) {
	registry := New()
	scope := SessionScope("s1")
	var calls []string
	testutil.FailErr(t, "register release rule", registry.Register(ScopeSession, "run", 10, func(context.Context, Scope) error {
		calls = append(calls, "run")
		return nil
	}))
	testutil.FailErr(t, "register disposal rule", registry.RegisterDisposal(ScopeSession, "lifetime", 20, func(context.Context, Scope) error {
		calls = append(calls, "lifetime")
		return nil
	}))
	testutil.FailErr(t, "release", registry.Release(context.Background(), scope))
	if !reflect.DeepEqual(calls, []string{"run"}) {
		t.Fatalf("release ran %v, want only the release rule", calls)
	}
	calls = nil
	testutil.FailErr(t, "dispose", registry.Dispose(context.Background(), scope))
	if !reflect.DeepEqual(calls, []string{"run", "lifetime"}) {
		t.Fatalf("dispose ran %v, want both rules in order", calls)
	}
}

func TestCanceledCleanupPreservesTrackedResourcesAndRegisteredRulesForRetry(t *testing.T) {
	for _, stopError := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(stopError.Error(), func(t *testing.T) {
			registry, scope := New(), DeviceScope()
			var calls []string
			first := true
			testutil.FailErr(t, "track earlier cleanup", registry.Track(scope, "earlier", 10, func(context.Context, Scope) error {
				calls = append(calls, "earlier")
				return nil
			}))
			testutil.FailErr(t, "register ordered drain", registry.RegisterDisposal(ScopeDevice, "drain", 20, func(context.Context, Scope) error {
				calls = append(calls, "drain")
				if first {
					first = false
					return fmt.Errorf("drain interrupted: %w", stopError)
				}
				return nil
			}))
			testutil.FailErr(t, "track deferred cleanup", registry.Track(scope, "deferred", 30, func(context.Context, Scope) error {
				calls = append(calls, "deferred")
				return nil
			}))
			testutil.FailErr(t, "register final cleanup", registry.RegisterDisposal(ScopeDevice, "final", 40, func(context.Context, Scope) error {
				calls = append(calls, "final")
				return nil
			}))
			if err := registry.Dispose(t.Context(), scope); !errors.Is(err, stopError) {
				t.Fatalf("interrupted disposal=%v", err)
			}
			if !reflect.DeepEqual(calls, []string{"earlier", "drain"}) {
				t.Fatalf("cleanup crossed interrupted drain: %v", calls)
			}
			testutil.FailErr(t, "retry disposal", registry.Dispose(t.Context(), scope))
			if !reflect.DeepEqual(calls, []string{"earlier", "drain", "drain", "deferred", "final"}) {
				t.Fatalf("retry lost or repeated tracked cleanup: %v", calls)
			}
		})
	}
}
