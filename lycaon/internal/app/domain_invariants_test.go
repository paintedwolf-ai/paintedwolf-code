package app

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/app/boards"
	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/server"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

// TestDomainBuilderStructLimits guarantees that ServeApp, serveBuilder, and the
// domain runtimes and dependency bags stay strictly within the 25-field limit.
func TestDomainBuilderStructLimits(t *testing.T) {
	cases := []struct {
		name     string
		instance any
		limit    int
	}{
		{"ServeApp", ServeApp{}, 25},
		{"serveBuilder", serveBuilder{}, 25},
		{"sessions.Runtime", sessions.Runtime{}, 25},
		{"sessions.Dependencies", sessions.Dependencies{}, 25},
		{"workflows.Runtime", workflows.Runtime{}, 25},
		{"workflows.Dependencies", workflows.Dependencies{}, 25},
		{"delegations.Runtime", delegations.Runtime{}, 25},
		{"delegations.Dependencies", delegations.Dependencies{}, 25},
		{"boards.Runtime", boards.Runtime{}, 25},
		{"boards.Dependencies", boards.Dependencies{}, 25},
		{"scanning.Runtime", scanning.Runtime{}, 25},
		{"scanning.Dependencies", scanning.Dependencies{}, 25},
		{"server.Runtime", server.Runtime{}, 25},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ := reflect.TypeOf(tc.instance)
			if typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			numFields := typ.NumField()
			if numFields > tc.limit {
				t.Fatalf("%s has %d fields, which exceeds the limit of %d", tc.name, numFields, tc.limit)
			}
		})
	}
}

// Runtime resources close in ascending disposal order: stop admission first, then storage.
func TestResourceLifetimeOrderInvariant(t *testing.T) {
	resources := &runtimeResources{lifecycle: resourcelifecycle.New()}

	var mu sync.Mutex
	var closedOrder []string

	resources.Track("low-priority-service", 10, func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		closedOrder = append(closedOrder, "low")
		return nil
	})

	resources.Track("mid-priority-service", 50, func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		closedOrder = append(closedOrder, "mid")
		return nil
	})

	resources.Track("high-priority-service", 90, func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		closedOrder = append(closedOrder, "high")
		return nil
	})

	resources.Close(context.Background())

	mu.Lock()
	defer mu.Unlock()

	expectedOrder := []string{"low", "mid", "high"}
	if len(closedOrder) != len(expectedOrder) {
		t.Fatalf("expected %d closed resources, got %d", len(expectedOrder), len(closedOrder))
	}
	for i, name := range expectedOrder {
		if closedOrder[i] != name {
			t.Errorf("expected close index %d to be %q, got %q", i, name, closedOrder[i])
		}
	}
}
