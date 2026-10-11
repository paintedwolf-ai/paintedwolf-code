package interactions

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

type testLifetime struct{ registry *resourcelifecycle.Registry }

func (l testLifetime) Track(name string, order int, cleanup func(context.Context) error) {
	_ = l.registry.Track(resourcelifecycle.DeviceScope(), name, order, func(ctx context.Context, _ resourcelifecycle.Scope) error { return cleanup(ctx) })
}
func TestAcquiredResourcesDisposeWhenSessionBindingFails(t *testing.T) {
	lifetime := testLifetime{registry: resourcelifecycle.New()}
	runtime := New(&events.Publisher{}, lifetime)
	failure := errors.New("session cleanup unavailable")
	err := runtime.Build(nil, nil, nil, func(string, int, func(context.Context, string) error) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("binding failure: %v", err)
	}
	if runtime.Processes == nil || runtime.Calls == nil || runtime.Pages == nil || runtime.Preview == nil {
		t.Fatal("expected acquired interaction resources")
	}
	if err := lifetime.registry.Dispose(context.Background(), resourcelifecycle.DeviceScope()); err != nil {
		t.Fatalf("partial build disposal: %v", err)
	}
	if err := lifetime.registry.Dispose(context.Background(), resourcelifecycle.DeviceScope()); err != nil {
		t.Fatalf("repeated disposal: %v", err)
	}
}
