package textutil

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSedScriptFilesAndBackupDoNotGatherWholeInputs(t *testing.T) {
	h := newHarness(t)
	h.owners.Revisions.Memory = h.memory
	h.owners.limits["whole_file_bytes"] = 8
	h.seed("extra", "first\x00\xff\nsecond\nthird\n")
	h.run("sed -n 'r extra'", "trigger\n").expectOutput(t, "raw script file", "first\x00\xff\nsecond\nthird\n", 0)
	h.run("sed -n 'R extra'", "one\ntwo\nthree\n").expectOutput(t, "record script file", "first\x00\xff\nsecond\nthird\n", 0)
	// A missing or unselected file is neither opened nor captured.
	before := len(h.pins.executions)
	h.run("sed -n 'Q;r missing'", "trigger\n").expectOutput(t, "unreached file", "", 0)
	if len(h.pins.executions) != before {
		t.Fatal("unreached script file gained a revision pin")
	}
	h.seed("original", "aaaaaaaaaaaaaaaa\nbbbbbbbbbbbbbbbb\n")
	h.run("sed -i.bak 's/a/A/g' original", "").expectOutput(t, "streamed original backup", "", 0)
	if h.read("original.bak") != "aaaaaaaaaaaaaaaa\nbbbbbbbbbbbbbbbb\n" || h.read("original") != "AAAAAAAAAAAAAAAA\nbbbbbbbbbbbbbbbb\n" {
		t.Fatal("backup replay lost original or edited bytes")
	}
	if h.memory.InUse().Working != 0 {
		t.Fatal("script file or backup retained working storage")
	}
	h.owners.limits["record_bytes"] = 8
	h.seed("wide", strings.Repeat("x", 16)+"\n")
	reject := h.run("sed -n 'R wide'", "trigger\n").expectRefusal(t, "script record bound", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "record_bytes" || reject.Data["maximum"] != int64(8) || reject.Data["observed"] != int64(16) {
		t.Fatalf("script record facts %+v", reject.Data)
	}
	testutil.FailErr(t, "close operation spills", h.spills.Close())
}
