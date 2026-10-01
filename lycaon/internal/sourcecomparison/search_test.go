package sourcecomparison

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindResumesRepeatedUnicodeMatchesAcrossFragments(t *testing.T) {
	text := strings.Repeat("🙂éneedle ", 900) + "\nlast needle\n"
	document, err := New(api.SourceComparisonSide{Content: text}, api.SourceComparisonSide{Content: text}, nil)
	testutil.FailErr(t, "prepare source", err)
	cursor := SearchCursor{}
	count := 0
	for {
		page, err := document.Find(t.Context(), "ÉNEEDLE", cursor, 37, false)
		testutil.FailErr(t, "read find page", err)
		count += len(page.Matches)
		if len(page.Matches) > 37 {
			t.Fatal("find exceeded page bound")
		}
		for _, match := range page.Matches {
			if match.To-match.From != 7 {
				t.Fatalf("UTF-16 match=%+v", match)
			}
		}
		if page.Complete {
			break
		}
		if page.Next.Row < cursor.Row || page.Next.Row == cursor.Row && page.Next.Byte <= cursor.Byte {
			t.Fatal("find cursor did not advance")
		}
		cursor = page.Next
	}
	if count != 900 {
		t.Fatalf("matches=%d", count)
	}
}

func TestFindReturnsProgressBeforeTraversingUnmatchedDocument(t *testing.T) {
	text := strings.Repeat(strings.Repeat("x", 1023)+"\n", 600) + "target\n"
	document, err := New(api.SourceComparisonSide{Content: text}, api.SourceComparisonSide{Content: text}, nil)
	testutil.FailErr(t, "prepare source", err)
	first, err := document.Find(t.Context(), "target", SearchCursor{}, 10, true)
	testutil.FailErr(t, "read bounded find batch", err)
	if first.Complete || first.Next.Row == 0 || len(first.Matches) != 0 {
		t.Fatalf("first batch=%+v", first)
	}
	cursor, count := first.Next, 0
	for {
		page, err := document.Find(t.Context(), "target", cursor, 10, true)
		testutil.FailErr(t, "continue find", err)
		count += len(page.Matches)
		if page.Complete {
			break
		}
		cursor = page.Next
	}
	if count != 1 {
		t.Fatalf("matches=%d", count)
	}
}
