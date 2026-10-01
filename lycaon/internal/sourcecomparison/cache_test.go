package sourcecomparison

import (
	"errors"
	"github.com/lycaon/lycaon/internal/pagedview"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCacheSharesPreparationWithoutCrossingAccessOrPresentation(t *testing.T) {
	var cache Cache
	before := api.SourceComparisonSide{Path: "example.go", Content: "package before\n"}
	after := api.SourceComparisonSide{Path: "example.go", Content: "package after\n"}
	const readers = 12
	documents := make([]*Document, readers)
	var group sync.WaitGroup
	for i := range documents {
		group.Add(1)
		go func() {
			defer group.Done()
			document, release, err := cache.Prepare(t.Context(), "owner", "project", before, after, nil)
			if err != nil {
				t.Errorf("prepare: %v", err)
				return
			}
			defer release()
			documents[i] = document
		}()
	}
	group.Wait()
	first := documents[0]
	if first == nil {
		t.Fatal("missing comparison")
	}
	for _, document := range documents {
		if document != first {
			t.Fatal("duplicate preparation")
		}
	}
	if len(first.rows) != first.Summary.Rows || !first.decorated {
		t.Fatal("published comparison lacks its prepared coordinate and syntax indexes")
	}
	before.VersionID = "another-reference-to-same-bytes"
	same, releaseSame, err := cache.Prepare(t.Context(), "owner", "project", before, after, nil)
	testutil.FailErr(t, "prepare alternate reference", err)
	defer releaseSame()
	if same != first {
		t.Fatal("version label prevented sharing")
	}
	for _, boundary := range [][2]string{{"another-person", "project"}, {"owner", "another-project"}} {
		distinct, release, err := cache.Prepare(t.Context(), boundary[0], boundary[1], before, after, nil)
		testutil.FailErr(t, "prepare isolated comparison", err)
		if distinct == first {
			t.Fatal("comparison shared across access boundaries")
		}
		release()
	}
	after.Path = "example.txt"
	other, releaseOther, err := cache.Prepare(t.Context(), "owner", "project", before, after, nil)
	testutil.FailErr(t, "prepare alternate language", err)
	defer releaseOther()
	if other == first {
		t.Fatal("different syntax reused presentation")
	}
	frameForTest(t, first, 0, 10, "full")
	if first.rows == nil || len(first.rows) != first.Summary.Rows {
		t.Fatal("content did not prepare its declared rows")
	}
}

func TestCacheSharesContentAcrossScreeningSnapshots(t *testing.T) {
	var cache Cache
	defer cache.Close()
	screen := &api.SecretScreen{Spans: []api.SecretSpan{{Start: 0, End: 6, State: api.SecretSpanDetected}}}
	endpoint := api.SourceComparisonSide{Path: "source.txt", Content: "secret\n", SecretScreen: screen}
	document, release, err := cache.Prepare(t.Context(), "person", "project", endpoint, endpoint, nil)
	testutil.FailErr(t, "prepare screened endpoint", err)
	defer release()
	if document.Before.SecretScreen != nil || document.After.SecretScreen != nil {
		t.Fatal("shared document retained presentation annotations")
	}
	endpoint.SecretScreen = &api.SecretScreen{Truncated: true}
	same, releaseSame, err := cache.Prepare(t.Context(), "person", "project", endpoint, endpoint, nil)
	testutil.FailErr(t, "prepare another screening snapshot", err)
	defer releaseSame()
	if same != document {
		t.Fatal("screening snapshot prevented content sharing")
	}
	if len(screen.Spans) != 1 {
		t.Fatal("preparation mutated its screening input")
	}
}

func TestComparisonCacheRejectsOversizeAndKeepsActiveReadsCharged(t *testing.T) {
	before := api.SourceComparisonSide{Content: "old\n"}
	after := api.SourceComparisonSide{Content: "new\n"}
	required := int64(4 << 20)
	budget := pagedview.NewBudget(required)
	cache := NewCache(budget)
	document, release, err := cache.Prepare(t.Context(), "owner", "project", before, after, nil)
	testutil.FailErr(t, "prepare pinned comparison", err)
	defer release()
	after.Content = strings.Repeat("larger comparison\n", 100_000)
	_, _, err = cache.Prepare(t.Context(), "owner", "project", before, after, nil)
	if !errors.Is(err, pagedview.ErrBudget) {
		t.Fatalf("oversize comparison: %v", err)
	}
	retained := document.retainedBytes()
	if budget.Used() != retained || retained >= required {
		t.Fatalf("retained charge=%d expected=%d limit=%d", budget.Used(), retained, required)
	}
	cache.Close()
	if budget.Used() != retained {
		t.Fatal("active read lost its reservation")
	}
	page := frameForTest(t, document, 0, 2, "full")
	if len(page.Rows) != 2 {
		t.Fatalf("release invalidated active content: %+v", page)
	}
	release()
	if budget.Used() != 0 {
		t.Fatal("finished read retained its reservation")
	}
}

func TestCacheAcceptsLargeTextWithoutReservingRenderedRows(t *testing.T) {
	line := "// " + strings.Repeat("x", 76) + "\n"
	text := strings.Repeat(line, (4<<20)/len(line))
	for _, changed := range []bool{false, true} {
		name := "unchanged"
		if changed {
			name = "changed"
		}
		t.Run(name, func(t *testing.T) {
			var cache Cache
			defer cache.Close()
			before := api.SourceComparisonSide{Path: "large.go", Content: text, Availability: "available"}
			after := before
			if changed {
				after.Content = strings.Replace(text, "xxxx", "yyyy", 1)
			}
			document, release, err := cache.Prepare(t.Context(), "person", "project", before, after, nil)
			testutil.FailErr(t, "retain large comparison", err)
			defer release()
			projection, err := document.Project(t.Context(), "full", nil, nil)
			testutil.FailErr(t, "project large comparison", err)
			rows, _, err := projection.Frame(t.Context(), int64(document.Summary.Rows-2), 2)
			testutil.FailErr(t, "read distant large comparison rows", err)
			if len(rows) != 2 || len(rows[0].Source.Syntax) == 0 {
				t.Fatalf("large comparison tail=%+v", rows)
			}
		})
	}
}

func TestComparisonReservationConvergesForShortLines(t *testing.T) {
	assertComparisonReservationConverges(t, 64<<10)
}

func assertComparisonReservationConverges(t *testing.T, bytes int) {
	t.Helper()
	const line = "const x = 1;\n"
	text := strings.Repeat(line, bytes/len(line))
	endpoint := api.SourceComparisonSide{Path: "large.ts", Content: text, Availability: "available"}
	cache := NewCache(pagedview.NewBudget(128 << 20))
	defer cache.Close()
	document, release, err := cache.Prepare(t.Context(), "person", "project", endpoint, endpoint, nil)
	testutil.FailErr(t, "prepare large short-line comparison", err)
	defer release()
	if used := cache.Budget().Used(); used != document.retainedBytes() || used >= document.decorationReservation() {
		t.Fatalf("retained budget did not converge: used=%d retained=%d working=%d", used, document.retainedBytes(), document.decorationReservation())
	}
}
