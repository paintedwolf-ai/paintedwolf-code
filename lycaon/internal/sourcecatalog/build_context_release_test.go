package sourcecatalog

import (
	"context"
	"runtime"
	"testing"
	"weak"
)

type callerValueKey struct{}

type callerHost struct{ services []string }

// The catalog is process-wide while the hosts whose requests start its builds
// are not: a published record must not keep its build's caller reachable.
func TestPublishedRecordReleasesBuildCaller(t *testing.T) {
	catalog := New()
	root := Root{ID: "r1", Path: t.TempDir()}
	key := rootKey("p1", root)

	caller := buildForCaller(t, catalog, key, root)
	runtime.GC()

	if caller.Value() != nil {
		t.Fatal("the published record keeps its build caller reachable")
	}
	if published := catalog.published(key); published.State != StateReady {
		t.Fatalf("published state = %q, want ready", published.State)
	}
}

// buildForCaller claims and publishes one build for a caller that the
// returned weak pointer observes.
func buildForCaller(t *testing.T, catalog *Catalog, key string, root Root) weak.Pointer[callerHost] {
	t.Helper()
	host := &callerHost{services: []string{"closed host"}}
	ctx := context.WithValue(t.Context(), callerValueKey{}, host)
	if claim := catalog.claim(ctx, key, "p1", []Root{root}); !claim.build {
		t.Fatal("a cold record did not start a build")
	}
	catalog.publish(key, Snapshot{Roots: []Root{root}}, nil)
	return weak.Make(host)
}
