package catalogruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func replaceItem[T any](_ Item[T], _ bool, incoming Item[T]) (Item[T], error) {
	return incoming, nil
}

func TestAssemblePreservesLayerOrderAndReplacesInPlace(t *testing.T) {
	cat, err := Assemble([]Layer[string]{
		{
			Name: "bundled",
			Items: []Item[string]{
				{ID: "zeta", Spec: "old"},
				{ID: "alpha", Spec: "first"},
			},
		},
		{
			Name:  "project",
			Items: []Item[string]{{ID: " zeta ", Spec: "new"}},
		},
	}, replaceItem[string])
	if err != nil {
		t.Fatalf("assemble catalog: %v", err)
	}
	if got, want := cat.IDs(), []string{"zeta", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %#v, want %#v", got, want)
	}
	item, ok := cat.Get("zeta")
	if !ok || item.Spec != "new" || item.ID != "zeta" {
		t.Fatalf("effective zeta = %#v, %v", item, ok)
	}
}

func TestAssembleMergeFailureIsTransactional(t *testing.T) {
	cat, err := Assemble([]Layer[string]{
		{Name: "bundled", Items: []Item[string]{{ID: "one"}}},
		{Name: "project", Items: []Item[string]{{ID: "one"}}},
	}, func(current Item[string], exists bool, incoming Item[string]) (Item[string], error) {
		if exists {
			return current, errors.New("replacement forbidden")
		}
		return incoming, nil
	})
	if err == nil || cat != nil {
		t.Fatalf("Assemble() = %#v, %v; want nil catalog and error", cat, err)
	}
}

func TestRegistryAndFactorySetUseStableExplicitKeys(t *testing.T) {
	registry := NewRegistry[int]()
	if err := registry.Register("zeta", 2); err != nil {
		t.Fatalf("register zeta: %v", err)
	}
	if err := registry.Register("alpha", 1); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := registry.Add("alpha", 3); err == nil {
		t.Fatal("Add accepted a duplicate stable id")
	}
	if got, want := registry.IDs(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %#v, want %#v", got, want)
	}

	factories := NewFactorySet(map[string]Factory[string, string]{
		"native": func(_ context.Context, spec string) (string, error) { return "native:" + spec, nil },
	}, func(_ context.Context, spec string) (string, error) { return "fallback:" + spec, nil })
	if got, err := factories.Build(context.Background(), "native", "x"); err != nil || got != "native:x" {
		t.Fatalf("native build = %q, %v", got, err)
	}
	if got, err := factories.Build(context.Background(), "compatible", "x"); err != nil || got != "fallback:x" {
		t.Fatalf("fallback build = %q, %v", got, err)
	}
}

func TestSnapshotCacheRejectsOldGenerationAndClonesValues(t *testing.T) {
	cache := NewSnapshotCache(time.Hour, func(in []string) []string {
		return append([]string(nil), in...)
	})
	_, present, _, generation := cache.Read()
	if present {
		t.Fatal("empty cache reported a value")
	}
	cache.Invalidate()
	if cache.Store([]string{"stale"}, generation) {
		t.Fatal("store from invalidated generation succeeded")
	}
	_, _, _, generation = cache.Read()
	input := []string{"current"}
	if !cache.Store(input, generation) {
		t.Fatal("store for current generation failed")
	}
	input[0] = "mutated"
	got, present, fresh, _ := cache.Read()
	if !present || !fresh || !reflect.DeepEqual(got, []string{"current"}) {
		t.Fatalf("Read() = %#v, %v, %v", got, present, fresh)
	}
	got[0] = "mutated again"
	again, _, _, _ := cache.Read()
	if !reflect.DeepEqual(again, []string{"current"}) {
		t.Fatalf("cache exposed mutable value: %#v", again)
	}
	cache.Expire()
	_, _, fresh, _ = cache.Read()
	if fresh {
		t.Fatal("expired cache reported fresh")
	}
	if !cache.BeginRefresh() || cache.BeginRefresh() {
		t.Fatal("refresh election did not select exactly one caller")
	}
	cache.EndRefresh()
	if !cache.BeginRefresh() {
		t.Fatal("refresh lease was not released")
	}
	cache.EndRefresh()
}
