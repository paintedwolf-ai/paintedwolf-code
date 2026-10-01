package sourcecomparison

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestCurrentSnapshotRejectsInvalidCoordinatesBeforeAllocatingRows(t *testing.T) {
	for _, length := range []uint64{1 << 30, 1 << 63} {
		memory, disk := pagedview.NewBudget(4<<20), pagedview.NewBudget(1<<20)
		document, err := NewCurrent(t.Context(), strings.NewReader("text\n"), textfile.UTF8, "source.txt", memory, disk)
		testutil.FailErr(t, "capture coordinate fixture", err)
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[:], length)
		_, err = document.index.WriteAt(encoded[:], 8)
		testutil.FailErr(t, "replace row length", err)
		_, _, err = document.Frame(t.Context(), 0, 1)
		document.Close()
		if !errors.Is(err, pagedview.ErrRange) {
			t.Fatalf("row length %d returned %v, want range refusal", length, err)
		}
	}
}

func TestCurrentSnapshotBoundsMemoryIndependentlyOfTextSize(t *testing.T) {
	memory, disk := pagedview.NewBudget(4<<20), pagedview.NewBudget(64<<20)
	line := strings.Repeat("x", 65535) + "\n"
	inputs := make([]io.Reader, 400)
	for i := range inputs {
		inputs[i] = strings.NewReader(line)
	}
	document, err := NewCurrent(t.Context(), io.MultiReader(inputs...), textfile.UTF8, "large.txt", memory, disk)
	testutil.FailErr(t, "capture large source", err)
	defer document.Close()
	if memory.Used() != currentMemoryBytes || document.Summary.After.Lines != 400 || document.Summary.Rows != 6400 {
		t.Fatalf("snapshot memory=%d summary=%+v", memory.Used(), document.Summary)
	}
	row, err := document.RowAtLine(t.Context(), 399)
	testutil.FailErr(t, "locate distant line", err)
	frame, total, err := document.Frame(t.Context(), int64(row), 16)
	testutil.FailErr(t, "read distant frame", err)
	var got strings.Builder
	for _, part := range frame {
		if part.Source.AfterLine != 399 || len(part.Source.Text) > 4096 {
			t.Fatalf("unbounded or wrong row: %+v", part)
		}
		got.WriteString(part.Source.Text)
	}
	if got.String() != line || total != 6400 {
		t.Fatal("distant frame changed source text")
	}
	document.Close()
	if memory.Used() != 0 || disk.Used() != 0 {
		t.Fatal("closed snapshot retained its memory or disk reservation")
	}
}

func TestCurrentSnapshotSearchCrossesFragmentsWithoutDuplicateMatches(t *testing.T) {
	memory, disk := pagedview.NewBudget(4<<20), pagedview.NewBudget(1<<20)
	text := strings.Repeat("x", 4094) + "🦊 needle\nneedle\r\nlast\r"
	document, err := NewCurrent(t.Context(), strings.NewReader(text), textfile.UTF8, "long.txt", memory, disk)
	testutil.FailErr(t, "capture fragmented text", err)
	defer document.Close()
	page, err := document.Find(t.Context(), "🦊 needle", SearchCursor{}, 1, true)
	testutil.FailErr(t, "find across fragments", err)
	if len(page.Matches) != 1 || page.Matches[0].Row != 1 || page.Matches[0].From != 0 {
		t.Fatalf("unicode match = %+v", page)
	}
	page, err = document.Find(t.Context(), "x🦊", SearchCursor{}, 1, true)
	testutil.FailErr(t, "find split match", err)
	if len(page.Matches) != 1 || page.Matches[0].Row != 0 || page.Matches[0].From != 4093 {
		t.Fatalf("crossing match = %+v", page)
	}
	page, err = document.Find(t.Context(), "x🦊", page.Next, 1, true)
	testutil.FailErr(t, "continue split search", err)
	if len(page.Matches) != 0 || !page.Complete {
		t.Fatalf("repeated crossing match = %+v", page)
	}
	if document.Summary.After.Lines != 3 {
		t.Fatalf("normalized lines = %d", document.Summary.After.Lines)
	}
}

func TestCurrentSnapshotFailureReleasesAllReservations(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		disk       int64
		want       error
	}{
		{"capacity", strings.Repeat("\n", 100), 100, pagedview.ErrBudget},
		{"malformed", "text\xff", 1000, textfile.ErrUnsupported},
		{"late binary", strings.Repeat("text", 4096) + "\x00", 1 << 20, textfile.ErrBinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory, disk := pagedview.NewBudget(4<<20), pagedview.NewBudget(tc.disk)
			_, err := NewCurrent(t.Context(), strings.NewReader(tc.text), textfile.UTF8, "bad.txt", memory, disk)
			if !errors.Is(err, tc.want) {
				t.Fatalf("capture error = %v, want %v", err, tc.want)
			}
			if memory.Used() != 0 || disk.Used() != 0 {
				t.Fatal("failed capture leaked its reservations")
			}
		})
	}
}
