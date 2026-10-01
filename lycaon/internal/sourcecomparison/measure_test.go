package sourcecomparison

import (
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/pkg/api"
)

// measureSides changes one line of a long file, so its changes view folds the
// unchanged run and its full view does not.
func measureSides() (api.SourceComparisonSide, api.SourceComparisonSide) {
	middle := strings.Repeat("const same = 1\n", 60)
	return api.SourceComparisonSide{Path: "example.go", Content: "package before\n" + middle},
		api.SourceComparisonSide{Path: "example.go", Content: "package after\n" + middle}
}

func TestMeasureSharesOneCountPerContentPair(t *testing.T) {
	var cache Cache
	before, after := measureSides()
	const callers = 16
	answers := make([]Measurement, callers)
	var group sync.WaitGroup
	for i := range answers {
		group.Add(1)
		go func() {
			defer group.Done()
			measured, err := cache.Measure(t.Context(), "owner", "project", before, after, nil, "changes", backgroundwork.PriorityProactive)
			if err != nil {
				t.Errorf("measure: %v", err)
				return
			}
			answers[i] = measured
		}()
	}
	group.Wait()
	first := answers[0]
	if first.Summary.Added == 0 && first.Summary.Removed == 0 {
		t.Fatalf("measured nothing: %+v", first)
	}
	if first.Extent.Rows == 0 {
		t.Fatalf("no display extent: %+v", first)
	}
	for _, measured := range answers {
		if !sameMeasurement(measured, first) {
			t.Fatalf("callers disagreed: %+v vs %+v", measured, first)
		}
	}
	// The second read is answered from the retained count, not measured again.
	repeat, err := cache.Measure(t.Context(), "owner", "project", before, after, nil, "changes", backgroundwork.PriorityProactive)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if !sameMeasurement(repeat, first) {
		t.Fatalf("repeat=%+v first=%+v", repeat, first)
	}
}

func TestMeasureSeparatesContentModeAndProject(t *testing.T) {
	var cache Cache
	before, after := measureSides()
	changes, err := cache.Measure(t.Context(), "owner", "project", before, after, nil, "changes", backgroundwork.PriorityProactive)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	full, err := cache.Measure(t.Context(), "owner", "project", before, after, nil, "full", backgroundwork.PriorityProactive)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if full.Extent.Rows <= changes.Extent.Rows {
		t.Fatalf("a folded comparison shows no more rows than the whole file: changes=%+v full=%+v", changes.Extent, full.Extent)
	}
	// Changed bytes make a different key; nothing needs invalidating.
	edited := after
	edited.Content = after.Content + "const extra = 5\n"
	moved, err := cache.Measure(t.Context(), "owner", "project", before, edited, nil, "changes", backgroundwork.PriorityProactive)
	if err != nil {
		t.Fatalf("edited: %v", err)
	}
	if moved.Summary.Added == changes.Summary.Added && moved.Summary.Removed == changes.Summary.Removed {
		t.Fatalf("edited content reused a stale count: %+v", moved.Summary)
	}
}

func TestMeasureReadsADocumentAReaderAlreadyPrepared(t *testing.T) {
	var cache Cache
	before, after := measureSides()
	document, release, err := cache.Prepare(t.Context(), "owner", "project", before, after, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer release()
	measured, err := cache.Measure(t.Context(), "owner", "project", before, after, nil, "changes", backgroundwork.PriorityProactive)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if measured.Summary.Added != document.Summary.Added || measured.Summary.Removed != document.Summary.Removed {
		t.Fatalf("measure=%+v document=%+v", measured.Summary, document.Summary)
	}
}

func sameMeasurement(a, b Measurement) bool {
	return a.Extent == b.Extent && a.Summary.Added == b.Summary.Added && a.Summary.Removed == b.Summary.Removed
}
