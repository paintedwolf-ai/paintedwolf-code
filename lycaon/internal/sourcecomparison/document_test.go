package sourcecomparison

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSummaryBoundsChangeAreasWithoutPreparingRows(t *testing.T) {
	var before, after strings.Builder
	for n := range 10 {
		fmt.Fprintf(&before, "old %d\nunchanged %d\n", n, n)
		fmt.Fprintf(&after, "new %d\nunchanged %d\n", n, n)
	}
	d, err := New(api.SourceComparisonSide{Content: before.String()}, api.SourceComparisonSide{Content: after.String()}, nil)
	testutil.FailErr(t, "prepare summary", err)
	if d.Summary.ChangeAreaCount != 10 || len(d.Summary.ChangeAreas) != 6 || d.Summary.Added != 10 || d.Summary.Removed != 10 {
		t.Fatalf("summary = %+v", d.Summary)
	}
	for index, area := range d.Summary.ChangeAreas {
		if area.AfterLine != index*2+1 || area.Added != 1 || area.Removed != 1 {
			t.Fatalf("area %d = %+v", index, area)
		}
	}
	if len(d.rows) != 0 {
		t.Fatal("summary prepared document rows")
	}
}

func TestDocumentRanges(t *testing.T) {
	before := strings.Repeat("unchanged\n", 80) + "old\n" + strings.Repeat("tail\n", 80)
	after := strings.Replace(before, "old\n", "new\n", 1)
	d, err := New(api.SourceComparisonSide{Content: before}, api.SourceComparisonSide{Content: after}, nil)
	testutil.FailErr(t, "compare", err)
	if d.Summary.Added != 1 || d.Summary.Removed != 1 {
		t.Fatalf("stats = %+v", d.Summary)
	}
	page := frameForTest(t, d, 0, 200, "changes")
	if !page.Complete || len(page.Rows) > 10 || page.Rows[0].Kind != "gap" {
		t.Fatalf("condensed page = %+v", page)
	}
	for _, side := range []string{"before", "after"} {
		var body strings.Builder
		for at := 0; ; {
			page := frameForTest(t, d, at, 7, side)
			for _, row := range page.Rows {
				body.WriteString(row.Text)
			}
			if page.Complete {
				break
			}
			if page.End <= at {
				t.Fatal("pagination did not advance")
			}
			at = page.End
		}
		want := before
		if side == "after" {
			want = after
		}
		if body.String() != want {
			t.Fatalf("%s reconstruction differs", side)
		}
	}
	matches := findForTest(t, d, "unchanged", SearchCursor{}, 7, true)
	if len(matches.Matches) != 7 || matches.Complete {
		t.Fatalf("find page = %+v", matches)
	}
}

func TestDocumentUnicodeAndLongLines(t *testing.T) {
	text := strings.Repeat("🙂", 100000) + " İİ match match\n"
	d, err := New(api.SourceComparisonSide{}, api.SourceComparisonSide{Content: text}, nil)
	testutil.FailErr(t, "compare", err)
	if d.Summary.Added != 1 {
		t.Fatalf("line count = %d", d.Summary.Added)
	}
	var body strings.Builder
	for at := 0; ; {
		page := frameForTest(t, d, at, 200, "after")
		bytes := 0
		for _, row := range page.Rows {
			bytes += len(row.Text)
			if !utf8.ValidString(row.Text) {
				t.Fatal("split rune")
			}
			body.WriteString(row.Text)
		}
		if bytes > len(page.Rows)*4096 {
			t.Fatalf("page bytes = %d", bytes)
		}
		if page.Complete {
			break
		}
		at = page.End
	}
	if body.String() != text {
		t.Fatal("long line reconstruction differs")
	}
	found := findForTest(t, d, "MATCH", SearchCursor{}, 1, false)
	if len(found.Matches) != 1 || found.Complete {
		t.Fatalf("first match = %+v", found)
	}
	next := findForTest(t, d, "MATCH", found.Next, 1, false)
	if len(next.Matches) != 1 {
		t.Fatalf("second match = %+v", next)
	}
	final := findForTest(t, d, "MATCH", next.Next, 1, false)
	if len(final.Matches) != 0 || !final.Complete {
		t.Fatalf("search end = %+v", final)
	}
}

func TestSummaryAndRowsAgreeAcrossTextBoundaries(t *testing.T) {
	cases := []struct {
		name, before, after string
		added, removed      int
	}{
		{"create", "", "new\n", 1, 0},
		{"delete", "old\n", "", 0, 1},
		{"empty", "", "", 0, 0},
		{"normalize", "one\r\ntwo\r", "one\ntwo\n", 0, 0},
		{"last newline", "one", "one\n", 1, 1},
		{"unicode", "🙂 old\n", "🙂 new\n", 1, 1},
		{"fragments", strings.Repeat("🙂", 3000), strings.Repeat("🙂", 3001), 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, removed, err := Counts(tc.before, tc.after)
			testutil.FailErr(t, "count lines", err)
			if added != tc.added || removed != tc.removed {
				t.Fatalf("counts +%d -%d, want +%d -%d", added, removed, tc.added, tc.removed)
			}
			d, err := New(api.SourceComparisonSide{Path: "source.go", Content: tc.before}, api.SourceComparisonSide{Path: "source.go", Content: tc.after}, nil)
			testutil.FailErr(t, "prepare summary", err)
			if d.rows != nil {
				t.Fatal("summary allocated rows")
			}
			d.prepare()
			if d.Summary.Rows != len(d.rows) || d.Summary.Added != added || d.Summary.Removed != removed {
				t.Fatalf("summary differs from prepared rows: %+v", d.Summary)
			}
			for _, side := range []string{"before", "after"} {
				page := frameForTest(t, d, 0, 200, side)
				var text strings.Builder
				for _, row := range page.Rows {
					text.WriteString(row.Text)
				}
				want := tc.before
				if side == "after" {
					want = tc.after
				}
				if !page.Complete || text.String() != normalize(want) {
					t.Fatalf("%s reconstruction differs", side)
				}
			}
		})
	}
}

func TestSplitRowsPreserveBothEndpointsAndSyntax(t *testing.T) {
	d, err := New(api.SourceComparisonSide{Path: "source.go", Content: "package before\n"}, api.SourceComparisonSide{Path: "source.go", Content: "package after\n"}, nil)
	testutil.FailErr(t, "prepare comparison", err)
	page := frameForTest(t, d, 0, 1, "split")
	if len(page.Rows) != 1 || page.Rows[0].Peer == nil {
		t.Fatalf("split row: %+v", page)
	}
	row := page.Rows[0]
	if row.Text != "package before\n" || row.Peer.Text != "package after\n" || row.BeforeLine != 1 || row.Peer.AfterLine != 1 {
		t.Fatalf("split coordinates: %+v", row)
	}
	if len(row.Syntax) == 0 || len(row.Peer.Syntax) == 0 || len(row.Changed) == 0 {
		t.Fatal("missing syntax or inline changes")
	}
}

func TestUnavailableBeforeDoesNotBecomeAnEmptyKnownFile(t *testing.T) {
	d, err := New(api.SourceComparisonSide{Availability: "not_captured"}, api.SourceComparisonSide{Availability: "available", Content: "retained\n"}, nil)
	testutil.FailErr(t, "prepare partial comparison", err)
	if d.Summary.Added != 0 || d.Summary.ChangeAreaCount != 0 {
		t.Fatal("unknown before was presented as a creation")
	}
	page := frameForTest(t, d, 0, 20, "changes")
	if len(page.Rows) != 1 || page.Rows[0].Kind != "equal" || page.Rows[0].Text != "retained\n" {
		t.Fatalf("readable side: %+v", page)
	}
	if len(frameForTest(t, d, 0, 20, "before").Rows) != 0 {
		t.Fatal("reader fabricated missing before content")
	}
}
