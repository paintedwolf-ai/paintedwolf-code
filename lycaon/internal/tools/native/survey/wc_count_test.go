package survey

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestCountBytesCountsOneCapturedRepresentation covers the shared wc
// contract: LF lines, ASCII-separated words, bytes, scalars and the longest
// line, independently of how a stream is chunked.
func TestCountBytesCountsOneCapturedRepresentation(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		want TextCounts
	}{
		"unterminated":   {"a\nb", TextCounts{Lines: 1, Words: 2, Bytes: 3, Chars: 3, MaxLineLength: 1}},
		"crlf":           {"one\r\ntwo\r\n", TextCounts{Lines: 2, Words: 2, Bytes: 10, Chars: 10, MaxLineLength: 3}},
		"empty":          {"", TextCounts{}},
		"separators":     {"  hello  world\n\tfoo\vbar\fbaz\r", TextCounts{Lines: 1, Words: 5, Bytes: 28, Chars: 28, MaxLineLength: 14}},
		"scalars":        {"héllo wörld\nñ\n", TextCounts{Lines: 2, Words: 3, Bytes: 17, Chars: 14, MaxLineLength: 11}},
		"invalid":        {"ok\xff\n", TextCounts{Lines: 1, Words: 1, Bytes: 4, Chars: 4, MaxLineLength: 3, InvalidEncoding: true}},
		"truncated":      {"a\xc3", TextCounts{Words: 1, Bytes: 2, Chars: 1, MaxLineLength: 1, InvalidEncoding: true}},
		"nul and binary": {"\x00\x01\n", TextCounts{Lines: 1, Words: 1, Bytes: 3, Chars: 3, MaxLineLength: 2}},
	} {
		if got := CountBytes([]byte(c.in)); got != c.want {
			t.Errorf("%s: CountBytes = %+v, want %+v", name, got, c.want)
		}
		var chunked Counter
		for i := 0; i < len(c.in); i++ {
			_, _ = chunked.Write([]byte{c.in[i]})
		}
		if got := chunked.Counts(); got != c.want {
			t.Errorf("%s: byte-at-a-time = %+v, want %+v", name, got, c.want)
		}
	}
}

// TestWcEntryNeverPairsEditorLinesWithDiskBytes: an open draft's counts all
// describe the draft; a file's describe the file.
func TestWcEntryNeverPairsEditorLinesWithDiskBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte("disk\n"), 0o644))
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat", err)
	reads := projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir))
	defer reads.Close()
	draft, err := wcFileEntry(t.Context(), reads, "notes.txt", path, info, true, sourceview.DraftOverlay{path: "one\ntwo\nthree"})
	testutil.FailErr(t, "count draft", err)
	if !draft.InEditor || draft.Bytes != 13 || *draft.Lines != 2 || *draft.Words != 3 {
		t.Fatalf("draft entry = %+v lines %d", draft, *draft.Lines)
	}
	disk, err := wcFileEntry(t.Context(), reads, "notes.txt", path, info, true, nil)
	testutil.FailErr(t, "count disk", err)
	if disk.InEditor || disk.Bytes != 5 || *disk.Lines != 1 {
		t.Fatalf("disk entry = %+v", disk)
	}
	var totals wcDirTotals
	totals.add(info.Size(), []byte("one\ntwo\nthree"), false, false, true, true)
	totals.add(info.Size(), []byte("disk\n"), false, false, false, true)
	totals.add(9, nil, true, false, false, true)
	if totals.bytes != 13+5+9 || totals.lines != 3 || totals.words != 4 || totals.files != 3 {
		t.Fatalf("totals = %+v", totals)
	}
}
