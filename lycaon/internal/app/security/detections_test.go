package security

import (
	"github.com/lycaon/lycaon/internal/detectionpack"
	"sync"
	"testing"
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
