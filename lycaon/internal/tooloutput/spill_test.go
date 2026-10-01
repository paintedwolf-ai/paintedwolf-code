package tooloutput

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestWireSpillToolOutputWritesInJailFile(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("x", 200)
	out := WireSpillToolOutput(dir, Screened(content), 50, 0)
	if !out.Truncated || out.SpillPath == "" {
		t.Fatalf("preview=%q spill=%q truncated=%v", out.Preview, out.SpillPath, out.Truncated)
	}
	if !IsAgentWireSpillRel(out.SpillPath) || !strings.HasPrefix(out.SpillPath, ToolOutputSpillDir+"/") {
		t.Fatalf("spill path %q is not a tool-output wire rel", out.SpillPath)
	}
	raw, err := os.ReadFile(DiskPath(dir, out.SpillPath))
	testutil.FailErr(t, "read spill", err)
	data, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress spill", err)
	if string(data) != content {
		t.Fatalf("spill content mismatch")
	}
}

func TestWireSpillToolOutputRefusesWithoutRecoveryStorage(t *testing.T) {
	content := strings.Repeat("y", 200)
	out := WireSpillToolOutput("", Screened(content), 50, 0)
	if out.RejectCode != ToolOutputSpillUnavailableCode || out.SpillPath != "" || out.Preview != "" {
		t.Fatalf("unrecoverable output was delivered: %+v", out)
	}
}

// Preview and spill content derive from one screened body.
func TestWireSpillToolOutputCutsPreviewFromTheSpilledBody(t *testing.T) {
	dir := t.TempDir()
	durable := "token=[REDACTED]\n" + strings.Repeat("x", 100)
	out := WireSpillToolOutput(dir, Screened(durable), 50, 0)
	if strings.Contains(out.Preview, "raw-secret") {
		t.Fatalf("preview = %q want screened bytes only", out.Preview)
	}
	head := strings.TrimSuffix(out.Preview, runeclamp.TruncatedSuffix)
	if !strings.HasPrefix(durable, head) {
		t.Fatalf("preview %q is not a cut of the spilled body %q", out.Preview, durable)
	}
	raw, err := os.ReadFile(DiskPath(dir, out.SpillPath))
	testutil.FailErr(t, "read spill", err)
	spill, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress spill", err)
	if string(spill) != durable {
		t.Fatalf("spill = %q want durable projection %q", spill, durable)
	}
}
