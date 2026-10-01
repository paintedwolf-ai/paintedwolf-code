package sourcecomparison

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectionAndSearchDoNotDecorateWholeDocument(t *testing.T) {
	text := strings.Repeat("const name = `value`;\n", 10000)
	document, err := New(api.SourceComparisonSide{Path: "example.ts", Content: text}, api.SourceComparisonSide{Path: "example.ts", Content: text}, nil)
	testutil.FailErr(t, "prepare comparison", err)
	_, err = document.Project(t.Context(), "full", nil, nil)
	testutil.FailErr(t, "project source", err)
	matches := findForTest(t, document, "value", SearchCursor{}, 1, true)
	if len(matches.Matches) != 1 {
		t.Fatal("search did not return a result")
	}
	if document.beforePlan.coordinates != nil || document.afterPlan.coordinates != nil {
		t.Fatal("metadata query initialized syntax decoration")
	}
	page := frameForTest(t, document, 9000, 2, "full")
	if len(page.Rows) != 2 || len(page.Rows[0].Syntax) == 0 {
		t.Fatal("requested range is missing syntax")
	}
	if document.inline.values != nil {
		t.Fatal("unchanged rows prepared inline comparison details")
	}
}

func TestNormalizedScreenMapsOnlySpanEndpoints(t *testing.T) {
	screen := &api.SecretScreen{Spans: []api.SecretSpan{
		{Start: 4, End: 8},
		{Start: 0, End: 2},
		{Start: 3, End: 4},
		{Start: 7, End: 99},
	}}
	mapped := normalizedScreen(screen, "😀a\r\nb\r\nc")
	expected := [][2]int{{3, 6}, {0, 2}, {3, 3}, {5, 6}}
	for i, span := range mapped.Spans {
		if span.Start != expected[i][0] || span.End != expected[i][1] {
			t.Fatalf("mapped span %d=%+v", i, span)
		}
	}
	if screen.Spans[0].Start != 4 || screen.Spans[3].End != 99 {
		t.Fatal("normalization changed retained source spans")
	}
}

func TestCanceledDecorationCanBePreparedByAnotherReader(t *testing.T) {
	text := strings.Repeat("const name = `value`;\n", 100)
	document, err := New(api.SourceComparisonSide{Path: "source.ts", Content: text}, api.SourceComparisonSide{Path: "source.ts", Content: text}, nil)
	testutil.FailErr(t, "create comparison", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := document.Decorate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decoration=%v", err)
	}
	testutil.FailErr(t, "prepare decoration for remaining reader", document.Decorate(t.Context()))
	projection, err := document.Project(t.Context(), "full", nil, nil)
	testutil.FailErr(t, "project comparison", err)
	rows, _, err := projection.Frame(t.Context(), 90, 2)
	testutil.FailErr(t, "read decorated rows", err)
	if len(rows) != 2 || len(rows[0].Source.Syntax) == 0 {
		t.Fatalf("decoration was poisoned: %+v", rows)
	}
}

func TestProjectionScreensPreserveCoordinatesAndOlderPresentations(t *testing.T) {
	screen := &api.SecretScreen{Spans: []api.SecretSpan{{Start: 3, End: 9, State: api.SecretSpanTracked}}}
	document, err := New(api.SourceComparisonSide{Content: "😀\r\nold\r\n"}, api.SourceComparisonSide{Content: "😀\r\nsecret\r\n", SecretScreen: screen}, nil)
	testutil.FailErr(t, "create comparison", err)
	original, err := document.Project(t.Context(), "split", nil, nil)
	testutil.FailErr(t, "project comparison", err)
	screened := original.WithSecretScreens(nil, screen)
	rows, _, err := screened.Frame(t.Context(), 0, 20)
	testutil.FailErr(t, "read screened projection", err)
	found := false
	for _, row := range rows {
		if peer := row.Source.Peer; peer != nil && peer.Text == "secret\n" {
			found = true
			if peer.SecretScreen == nil || len(peer.SecretScreen.Spans) != 1 || peer.SecretScreen.Spans[0].Start != 0 || peer.SecretScreen.Spans[0].End != 6 {
				t.Fatalf("screened peer coordinates = %+v", peer.SecretScreen)
			}
		}
	}
	if !found {
		t.Fatal("missing replacement peer")
	}
	rows, _, err = original.Frame(t.Context(), 0, 20)
	testutil.FailErr(t, "read original projection", err)
	for _, row := range rows {
		if row.Source.SecretScreen != nil || row.Source.Peer != nil && row.Source.Peer.SecretScreen != nil {
			t.Fatal("screening changed the original projection")
		}
	}
	if screen.Spans[0].Start != 3 || screen.Spans[0].End != 9 {
		t.Fatal("projection normalization mutated its screening input")
	}
}
