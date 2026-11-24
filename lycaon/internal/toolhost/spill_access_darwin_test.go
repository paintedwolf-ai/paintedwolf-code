package toolhost

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

// Seatbelt denies the state tree to a child, and the exact-file read grant is
// dropped there rather than opening host data, so conversion alone never
// exposes the file.
func TestSeatbeltChildCannotOpenAConvertedSpill_ACSPILL01(t *testing.T) {
	f := newSpillFixture(t)
	source := `/bin/cat "$(native_path -- ` + f.rel + `)"`
	if out := f.run(t, source); out.Status == 0 || out.Stdout != "" {
		t.Fatalf("child opened the spill: %+v", out)
	}
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.HostDataDir = f.host
		tc.Files.SessionReadPaths = []string{f.abs}
	}
	if out := f.run(t, source); out.Status == 0 || strings.Contains(out.Stdout, "alpha") {
		t.Fatalf("exact grant opened the state tree: %+v", out)
	}
}
