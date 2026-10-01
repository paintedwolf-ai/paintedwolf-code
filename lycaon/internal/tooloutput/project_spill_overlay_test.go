package tooloutput_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestSpillOverlayPromoteProjectUsesInlineDigest(t *testing.T) {
	dir := t.TempDir()
	full := strings.Repeat("x", 8192) + `{"job_id":"job-1","conflicts":[{"path":"a.go"}]}`
	inline := `{"job_id":"job-1","conflict_digest":[{"path":"a.go","summary":["L1: primary: p"]}]}`
	rel := tooloutput.PromoteSpillRelPath("job-1")
	out := tooloutput.WireSpillOverlayPromote(dir, rel, tooloutput.Screened(full), tooloutput.Screened(inline), 4096, 0)
	if !out.Truncated || out.SpillPath == "" {
		t.Fatalf("expected spill truncated=%v rel=%q", out.Truncated, out.SpillPath)
	}
	if out.SpillPath != rel {
		t.Fatalf("spill=%q want %q", out.SpillPath, rel)
	}
	if out.Preview != inline {
		t.Fatalf("preview should prefer inline digest, got len=%d", len(out.Preview))
	}
}

func TestSpillOverlayPromoteProjectPreservesWorkerSpill(t *testing.T) {
	dir := t.TempDir()
	rel := tooloutput.PromoteSpillRelPath("job-1")
	abs := tooloutput.DiskPath(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	workerSpill := `{"job_id":"job-1","conflicts":[{"path":"a.go","base":"base body","primary":"primary body","branch":"branch body"}]}`
	if err := os.WriteFile(abs, []byte(workerSpill), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	full := strings.Repeat("x", 8192) + `{"job_id":"job-1","conflicts":[{"path":"a.go"}]}`
	inline := `{"job_id":"job-1","conflict_digest":[{"path":"a.go","summary":["L1: primary: p"]}]}`
	out := tooloutput.WireSpillOverlayPromote(dir, rel, tooloutput.Screened(full), tooloutput.Screened(inline), 4096, 0)
	if !out.Truncated || out.SpillPath != rel {
		t.Fatalf("truncated=%v relOut=%q want %q", out.Truncated, out.SpillPath, rel)
	}
	if out.Preview != inline {
		t.Fatalf("preview should use inline digest, got %q", out.Preview)
	}
	got, err := os.ReadFile(abs)
	testutil.FailErr(t, "read file", err)
	if string(got) != workerSpill {
		t.Fatalf("worker spill overwritten: %q", string(got))
	}
}
