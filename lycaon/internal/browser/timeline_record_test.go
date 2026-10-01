package browser

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

// The sampler reads text geometry when the page reports it may have moved, and at least
// once per floor; a still page is not walked every tick.
func TestRegionSamplerFollowsThePagesGeometryEpoch(t *testing.T) {
	var samples atomic.Int32
	s := &recorderSession{collectRegions: func(*rod.Page) PageRegions {
		samples.Add(1)
		return PageRegions{Complete: true}
	}}
	stop := s.sampleRegions(t.Context(), nil)
	time.Sleep(4 * regionSampleInterval)
	if got := samples.Load(); got != 1 {
		t.Fatalf("a still page was sampled %d times in %v, want the opening sample only", got, 4*regionSampleInterval)
	}
	s.telemetry(`[{"kind":"geometry","t":0,"epoch":1}]`)
	time.Sleep(3 * regionSampleInterval)
	if got := samples.Load(); got != 2 {
		t.Fatalf("a geometry epoch did not cause one sample: %d", got)
	}
	s.telemetry(`[{"kind":"geometry","t":0,"epoch":1}]`)
	time.Sleep(3 * regionSampleInterval)
	if got := samples.Load(); got != 2 {
		t.Fatalf("an unchanged epoch caused a sample: %d", got)
	}
	stop()
	if got := samples.Load(); got != 3 {
		t.Fatalf("stopping did not take the closing sample: %d", got)
	}
	if len(s.regions) != 3 || s.regions[0].key == "" || s.regions[0].key != s.regions[2].key {
		t.Fatalf("samples of the same geometry should share a key: %+v", s.regions)
	}
	if len(s.events) != 0 {
		t.Fatalf("geometry reports are not timeline events: %+v", s.events)
	}
}
