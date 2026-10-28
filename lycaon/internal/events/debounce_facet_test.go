package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Distinct settings areas retain separate debounce keys.
func TestSettingsAreasDoNotCoalesce(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	for _, area := range []api.SettingsArea{api.SettingsAreaVerify, api.SettingsAreaApprovals} {
		if err := hub.Publish(ctx, api.EventTopicSettings, PublishKey{Facet: string(area)}, api.SettingsEvent{
			Area:   area,
			Action: "updated",
		}); err != nil {
			t.Fatalf("publish %s: %v", area, err)
		}
	}
	hub.FlushDebounced()

	seen := map[api.SettingsArea]bool{}
	for len(seen) < 2 {
		select {
		case env := <-ch:
			var ev api.SettingsEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				t.Fatalf("decode: %v", err)
			}
			seen[ev.Area] = true
		case <-t.Context().Done():
			t.Fatalf("only saw %v; a settings area was coalesced away", seen)
		}
	}
}

// Repeated writes to one settings area share a delivery.
func TestSameSettingsAreaStillCoalesces(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	for range 5 {
		if err := hub.Publish(ctx, api.EventTopicSettings, PublishKey{Facet: string(api.SettingsAreaVerify)}, api.SettingsEvent{
			Area:   api.SettingsAreaVerify,
			Action: "updated",
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	hub.FlushDebounced()

	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatal("no settings event delivered")
	}
	select {
	case env := <-ch:
		t.Fatalf("repeated writes to one area should coalesce, got a second delivery: %+v", env)
	default:
	}
}

// Repeated writes to one path collapse to its latest state.
func TestSourceChangedCoalescesPerPath(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	const path = ".task/last-run/den-scroll.jsonl"
	for range 50 {
		if err := hub.Publish(ctx, api.EventTopicSourceChanged, PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Facet: path}, api.SourceChangesEvent{
			ProjectID: "84a676a3-ccd2-5967-97ed-fd384c0b9003",
			Changes:   []api.SourceChange{{Path: path, Op: "write"}},
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	hub.FlushDebounced()

	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatal("no source_changed event delivered")
	}
	select {
	case env := <-ch:
		t.Fatalf("50 writes to one path should coalesce, got a second delivery: %+v", env)
	default:
	}
}

func TestSourceChangedKeepsDistinctPaths(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	paths := []string{"src/a.go", "src/b.go", "src/c.go"}
	for _, p := range paths {
		if err := hub.Publish(ctx, api.EventTopicSourceChanged, PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Facet: p}, api.SourceChangesEvent{
			ProjectID: "84a676a3-ccd2-5967-97ed-fd384c0b9003",
			Changes:   []api.SourceChange{{Path: p, Op: "write"}},
		}); err != nil {
			t.Fatalf("publish %s: %v", p, err)
		}
	}
	hub.FlushDebounced()

	seen := map[string]bool{}
	for len(seen) < len(paths) {
		select {
		case env := <-ch:
			var ev api.SourceChangesEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(ev.Changes) != 1 {
				t.Fatalf("changes = %d, want 1", len(ev.Changes))
			}
			seen[ev.Changes[0].Path] = true
		case <-t.Context().Done():
			t.Fatalf("only saw %v; a distinct file was coalesced away", seen)
		}
	}
}
