package native

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// A finished command whose tail dropped output says so and names the file
// holding all of it; a complete tail claims nothing.
func TestCommandResultStatesTruncationAndSpill(t *testing.T) {
	full := "stdout: " + strings.Repeat("line\n", 100)
	tail := full[len(full)-64:]
	res := commandResultFromOutcome(commandRunOutcome{
		Finished:  true,
		Snapshot:  bgprocess.Snapshot{Tail: tail, Output: full, OutputScreened: true},
		SpillPath: "tool-output/abc.txt",
	}, confine.LocalNetworkGrant{}, nil)
	if !res.Truncated || res.OriginalTailBytes != len(full) || res.WireSpillPath != "tool-output/abc.txt" {
		t.Fatalf("result = truncated:%v original:%d spill:%q", res.Truncated, res.OriginalTailBytes, res.WireSpillPath)
	}

	complete := commandResultFromOutcome(commandRunOutcome{
		Finished: true,
		Snapshot: bgprocess.Snapshot{Tail: "stdout: ok\n", Output: "stdout: ok\n", OutputScreened: true},
	}, confine.LocalNetworkGrant{}, nil)
	if complete.Truncated || complete.OriginalTailBytes != 0 || complete.WireSpillPath != "" {
		t.Fatalf("complete tail overclaimed: %+v", complete)
	}

	evicted := commandResultFromOutcome(commandRunOutcome{
		Finished: true,
		Snapshot: bgprocess.Snapshot{Tail: "x", Output: "x", OutputScreened: true, OutputEvicted: true},
	}, confine.LocalNetworkGrant{}, nil)
	if !evicted.Truncated {
		t.Fatal("a ring that evicted bytes cannot present its tail as complete")
	}
}

func TestSpillCommandOutputWritesTheWholeScreenedBody(t *testing.T) {
	dir := t.TempDir()
	tctx := testToolContext(t.TempDir())
	tctx.Host.HostDataDir = dir
	full := "stdout: " + strings.Repeat("row\n", 50)
	snap := bgprocess.Snapshot{Tail: full[len(full)-32:], Output: full, OutputScreened: true}

	rel := spillCommandOutput(tctx, snap)
	if rel == "" || !tooloutput.IsAgentWireSpillRel(rel) {
		t.Fatalf("spill path = %q", rel)
	}
	raw, err := os.ReadFile(tooloutput.DiskPath(dir, rel))
	testutil.FailErr(t, "read spill", err)
	data, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress spill", err)
	if string(data) != full {
		t.Fatal("spill must hold the exact screened output")
	}

	if spillCommandOutput(tctx, bgprocess.Snapshot{Tail: full, Output: full, OutputScreened: true}) != "" {
		t.Fatal("a complete tail needs no spill")
	}
	if spillCommandOutput(tctx, bgprocess.Snapshot{Tail: "x", Output: full, OutputScreened: false}) != "" {
		t.Fatal("unscreened output must never leave the process")
	}
	tctx.Host.MaxToolSpillBytes = len(full) - 1
	if spillCommandOutput(tctx, snap) != "" {
		t.Fatal("output beyond the retention bound must not advertise whole-output recovery")
	}
	tctx.Host.MaxToolSpillBytes = len(full)
	if spillCommandOutput(tctx, snap) != rel {
		t.Fatal("output at the retention bound lost its recovery reference")
	}
}
