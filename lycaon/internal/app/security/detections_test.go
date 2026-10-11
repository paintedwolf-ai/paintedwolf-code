package security

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/testutil"
	"runtime"
	"slices"
	"sync"
	"testing"
	"weak"
)

func TestDetectionPublicationRetainsCompleteGenerations(t *testing.T) {
	t.Parallel()
	var runtime Detections
	if runtime.GateSource() != nil || runtime.MintedCredentialSource() != nil || runtime.EgressSource() != nil {
		t.Fatal("unpublished catalog reported a source")
	}
	matcher := detectionpack.NewMatcher(&detectionpack.Catalog{})
	runtime.Publish(matcher)
	original := runtime.current.Load()
	if original.matcher != matcher || original.gate != runtime.GateSource() || original.gate != runtime.MintedCredentialSource() || original.egress != runtime.EgressSource() {
		t.Fatal("consumers disagree on the published generation")
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			runtime.Publish(nil)
			runtime.Publish(matcher)
		}
	})
	for range 3 {
		workers.Go(func() {
			for range 100 {
				current := runtime.current.Load()
				if current != nil && (current.matcher == nil || current.gate == nil || current.egress == nil) {
					t.Error("published an incomplete generation")
				}
			}
		})
	}
	workers.Wait()
	runtime.Publish(nil)
	if runtime.GateSource() != nil || runtime.MintedCredentialSource() != nil || runtime.EgressSource() != nil {
		t.Fatal("withdrawn catalog reported a source")
	}
	if original.matcher != matcher || original.gate == nil || original.egress == nil {
		t.Fatal("publication changed a retained generation")
	}
}

func installRuntimeFloors(t *testing.T) weak.Pointer[Runtime] {
	t.Helper()
	security := New(t.Context(), nil, nil, nil, nil)
	testutil.FailErr(t, "load permanent credential floors", security.Detections.LoadFloors())
	return weak.Make(security)
}

func TestCredentialFloorDoesNotRetainItsParentRuntime(t *testing.T) {
	retained := installRuntimeFloors(t)
	before := confine.CredentialStorePaths()
	if len(before) == 0 {
		t.Fatal("loaded credential floor is empty")
	}
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil); confine.SetKeyMaterialPathsSource(nil) })
	for range 10 {
		runtime.GC()
		if retained.Value() == nil {
			break
		}
	}
	if retained.Value() != nil {
		t.Fatal("process credential floor retains its parent security runtime")
	}
	if after := confine.CredentialStorePaths(); !slices.Equal(before, after) {
		t.Fatalf("collecting the parent changed the credential floor: before=%v after=%v", before, after)
	}
}
