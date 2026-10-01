package tooloutput

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func readSpill(t *testing.T, dir, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(DiskPath(dir, rel))
	testutil.FailErr(t, "read spill", err)
	data, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress spill", err)
	return string(data)
}

func TestLineAddressableToolJSONPutsElementsOnTheirOwnLines(t *testing.T) {
	content := "[git#2]\n" + `{"available":true,"files":[{"path":"a.go","insertions":1},{"path":"b.go","insertions":2}]}` + "\n>>> host banner"
	out := LineAddressableToolJSON(content)
	if !strings.HasPrefix(out, "[git#2]\n{") || !strings.HasSuffix(out, "\n>>> host banner") {
		t.Fatalf("prefix or suffix lost: %q", out)
	}
	lines := strings.Split(out, "\n")
	var pathLines []int
	for i, l := range lines {
		if strings.Contains(l, `"path"`) {
			pathLines = append(pathLines, i)
		}
	}
	if len(pathLines) != 2 || pathLines[0] == pathLines[1] {
		t.Fatalf("each element must land on its own line: %q", out)
	}
	if LineAddressableToolJSON("stdout: not json at all") != "stdout: not json at all" {
		t.Fatal("raw text must pass through unchanged")
	}
}

// A tool wire body spills line-addressable so `read` paging over the spill
// lands on elements; a raw body spills byte-exact.
func TestWireSpillToolOutputWritesLineAddressableJSON(t *testing.T) {
	dir := t.TempDir()
	body := "[git#2]\n" + `{"files":[` + strings.Repeat(`{"path":"a.go"},`, 40) + `{"path":"z.go"}]}`
	out := SpillWholeToolOutput(dir, Screened(body), 0)
	if out.SpillPath == "" || !out.Truncated || out.OriginalBytes != len(body) || out.Preview != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.SpillPath != ToolOutputSpillRelPath(body) {
		t.Fatal("the spill is addressed by the wire body, not by its rendering")
	}
	got := readSpill(t, dir, out.SpillPath)
	if strings.Count(got, "\n") < 40 {
		t.Fatalf("spill is not line-addressable: %d newlines", strings.Count(got, "\n"))
	}

	raw := "stdout: line one\nline two\n"
	exact := SpillWholeRaw(dir, Screened(raw), 0)
	if readSpill(t, dir, exact.SpillPath) != raw {
		t.Fatal("a raw body must spill byte-exact")
	}
}

func TestPartialSpillCannotReplaceFullObservation(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("observed line\n", 1000)
	full := SpillWholeRaw(dir, Screened(body), 0)
	if full.SpillPath == "" {
		t.Fatal("full observation was not retained")
	}
	for _, spill := range []func(string, ScreenedOutput, int) WireSpillOutcome{SpillWholeRaw, SpillWholeToolOutput} {
		out := spill(dir, Screened(body), 512)
		if out.SpillPath != "" || out.RejectCode != ToolOutputSpillCapExceededCode {
			t.Fatalf("whole retention accepted partial output: %+v", out)
		}
	}
	partial := WireSpillToolOutput(dir, Screened(body), 80, 512)
	if !partial.SpillCapped || partial.SpillPath == "" || partial.SpillPath == full.SpillPath {
		t.Fatalf("partial and full observations share an address: %+v", partial)
	}
	if got := readSpill(t, dir, partial.SpillPath); len(got) > 512 || got == body {
		t.Fatal("partial spill exceeded its retention bound")
	}
	if readSpill(t, dir, full.SpillPath) != body {
		t.Fatal("a smaller retention bound replaced the captured observation")
	}
}
