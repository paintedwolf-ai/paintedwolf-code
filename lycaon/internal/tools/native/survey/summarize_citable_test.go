package survey

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// A window summarize returns is what becomes citable, so a hit must not join
// rows that a numbered window then refuses.
func TestSummarizePatternWindowsAreConsecutive(t *testing.T) {
	dir := t.TempDir()
	var source strings.Builder
	source.WriteString("package far\n\nfunc Hit() {}\n")
	for range 400 {
		source.WriteString("// filler\n")
	}
	source.WriteString("func AnotherHit() {}\n")
	writeFile(t, dir, "far.go", source.String())
	tool := &SummarizeTool{Boundary: nativefixture.Boundary(t), Caps: summarize.DefaultCaps()}
	_, windows, err := tool.Summarize(t.Context(), SummarizeRequest{Task: "hits", Path: "far.go", Pattern: "Hit"}, nativefixture.Context(dir))
	testutil.FailErr(t, "summarize", err)
	if len(windows) == 0 {
		t.Fatal("no windows returned")
	}
	for i, window := range windows {
		if _, err := evidence.ConfirmNumberedWindow(window.Path, window.Body, "response", "delivery"); err != nil {
			t.Errorf("window %d of %s would lose its citation: %v\n%s", i, window.Path, err, window.Body)
		}
	}
}
