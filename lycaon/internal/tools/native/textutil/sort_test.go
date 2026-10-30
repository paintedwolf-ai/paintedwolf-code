//go:build darwin || linux

package textutil

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandspill"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSortGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"sort", "b\na\nc\n", "a\nb\nc\n", 0},
		{"sort", "b\nB\na\nA\n_\n", "A\nB\n_\na\nb\n", 0},
		{"sort", "é\ne\nz\n\xff\n", "e\nz\né\n\xff\n", 0},
		{"sort -r", "a\nc\nb\n", "c\nb\na\n", 0},
		{"sort", "b\na", "a\nb\n", 0},
		{"sort", "", "", 0},
		{"sort -u", "b\na\nb\na\n", "a\nb\n", 0},
		{"sort -n", "10\n9\n-1\n1.5\n1.25\nx\n", "-1\nx\n1.25\n1.5\n9\n10\n", 0},
		{"sort -n", "007\n7\n-0\n0\n", "-0\n0\n007\n7\n", 0},
		{"sort -nu", "007\n7\n-0\n0\n", "-0\n007\n", 0},
		{"sort -n", "123456789012345678901234567890\n123456789012345678901234567891\n2\n", "2\n123456789012345678901234567890\n123456789012345678901234567891\n", 0},
		{"sort -t : -k 2", "a:3\nb:1\nc:2\n", "b:1\nc:2\na:3\n", 0},
		{"sort -t : -k 2n", "a:10\nb:9\n", "b:9\na:10\n", 0},
		{"sort -t : -k 2,2 -k 1r", "b:x\na:x\nc:a\n", "c:a\nb:x\na:x\n", 0},
		{"sort -k 2", "x b\ny  a\nz c\n", "y  a\nx b\nz c\n", 0},
		{"sort -k 1.2,1.2", "ab\nba\naa\n", "aa\nba\nab\n", 0},
		{"sort -z", "b\x00a\x00c", "a\x00b\x00c\x00", 0},
		{"sort -z", "x\ny\x00a\nb\x00", "a\nb\x00x\ny\x00", 0},
	})
}

func TestSortOperandsAndRefusals(t *testing.T) {
	h := newHarness(t)
	h.seed("one.txt", "c\na\n")
	h.seed("two.txt", "b\n")
	h.run("sort one.txt - two.txt", "d\n").expectOutput(t, "files and stdin", "a\nb\nc\nd\n", 0)
	r := h.run("sort one.txt missing.txt", "")
	r.expectOutput(t, "missing input", "", 2)
	if r.stderr != "sort: missing.txt: No such file or directory\n" {
		t.Fatalf("stderr %q", r.stderr)
	}
	for _, source := range []string{"sort -k 0", "sort -k 1.0", "sort -k x", "sort -k 1q", "sort -t ab", "sort -k +1"} {
		h.run(source, "").expectRefusal(t, source, "COMMAND_OPERAND_INVALID")
	}
	h.run("sort -R", "").expectRefusal(t, "sort -R", "COMMAND_OPTION_UNSUPPORTED")
}

// Input past sort_memory_bytes spills sorted runs, merges with bounded fan-in
// and produces the same bytes as an in-memory sort; spills are removed.
func TestSortSpillsAndMergesInBoundedPasses(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["sort_memory_bytes"] = 4096
	var input strings.Builder
	var lines []string
	for i := 0; i < 3000; i++ {
		line := fmt.Sprintf("%05d-%s", (i*7919)%3001, strings.Repeat("x", i%13))
		lines = append(lines, line)
		input.WriteString(line + "\n")
	}
	slices.Sort(lines)
	want := strings.Join(lines, "\n") + "\n"
	h.run("sort", input.String()).expectOutput(t, "spilled sort", want, 0)
	if h.quota.reservations == 0 || h.quota.used != 0 {
		t.Fatalf("spill admission reservations=%d still held=%d", h.quota.reservations, h.quota.used)
	}
	entries, err := os.ReadDir(h.spills)
	testutil.FailErr(t, "read spill dir", err)
	if len(entries) != 0 {
		t.Fatalf("spill runs left behind: %d", len(entries))
	}
	unique := h.run("sort -u -r", input.String()+input.String())
	slices.Reverse(lines)
	unique.expectOutput(t, "spilled unique reverse", strings.Join(slices.Compact(lines), "\n")+"\n", 0)
}

func TestSortSpillBoundaries(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["sort_memory_bytes"] = 256
	input := strings.Repeat("abcdefghij\n", 100)
	h.quota.maximum = 0
	var exhausted *toolcontract.ProgramMemoryLimitError
	if r := h.run("sort", input); !asMemoryLimit(r.err, &exhausted) || r.result.Terminal != toolcontract.ProducerFailed || exhausted.Account != "session_spill_bytes" {
		t.Fatalf("spill quota: %+v %v", r.result, r.err)
	}
	h.quota.maximum = 1 << 30
	h.owners.limits["spill_bytes"] = 64
	reject := h.run("sort", input).expectRefusal(t, "spill_bytes", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "spill_bytes" {
		t.Fatalf("limit %+v", reject.Data)
	}
	entries, _ := os.ReadDir(h.spills)
	if len(entries) != 0 || h.quota.used != 0 {
		t.Fatalf("refused sort left %d runs and %d admitted bytes", len(entries), h.quota.used)
	}
	h.owners.limits["spill_bytes"] = 1 << 28
	noSpill := newHarness(t)
	noSpill.owners.limits["sort_memory_bytes"] = 256
	ports := Ports{ToolContext: noSpill.tc, Memory: noSpill.memory}
	r := noSpill.runWith(ports, "sort", input)
	if reject := r.expectRefusal(t, "no spill storage", "COMMAND_LIMIT_EXCEEDED"); reject.Data["limit_id"] != "sort_memory_bytes" {
		t.Fatalf("limit %+v", reject.Data)
	}
}

func TestRecordBytesBoundary(t *testing.T) {
	h := newHarness(t)
	limit := int(h.owners.limits["record_bytes"])
	at := strings.Repeat("a", limit)
	h.run("sort", at+"\n").expectOutput(t, "record at limit", at+"\n", 0)
	h.run("sort", at).expectOutput(t, "unterminated record at limit", at+"\n", 0)
	reject := h.run("sort", at+"a\n").expectRefusal(t, "record over limit", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "record_bytes" {
		t.Fatalf("limit %+v", reject.Data)
	}
	h.run("uniq", at+"a").expectRefusal(t, "uniq record over limit", "COMMAND_LIMIT_EXCEEDED")
}

func TestStreamBytesBoundary(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["stream_bytes"] = 6
	h.run("echo abcde", "").expectOutput(t, "stdout at limit", "abcde\n", 0)
	reject := h.run("echo abcdef", "").expectRefusal(t, "stdout over limit", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "stream_bytes" || reject.Data["observed"] != int64(7) {
		t.Fatalf("limit %+v", reject.Data)
	}
}

func TestWorkingMemoryRefusal(t *testing.T) {
	h := newHarness(t)
	small, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1024, State: 1 << 20})
	testutil.FailErr(t, "small memory", err)
	r := h.runWith(Ports{ToolContext: h.tc, Memory: small}, "sort", "a\n")
	var exhausted *toolcontract.ProgramMemoryLimitError
	if r.err == nil || !asMemoryLimit(r.err, &exhausted) || small.InUse().Working != 0 {
		t.Fatalf("err %v in use %+v", r.err, small.InUse())
	}
}

// A spilled sort keeps its runs in the program's private directory under the
// session TMPDIR; settling the program and host loss both remove them.
func TestSessionSpillsLiveUnderTheSessionAndAreReclaimed(t *testing.T) {
	h := newHarness(t)
	scratch := t.TempDir()
	h.tc.Host.SessionScratchDir = scratch
	spills, err := commandspill.NewSessionSpills(h.tc.Host.SessionScratchDir, "program-1")
	testutil.FailErr(t, "session spills", err)
	h.owners.limits["sort_memory_bytes"] = 512
	ports := h.ports()
	ports.Spills = spills
	input := strings.Repeat("b\na\nc\n", 200)
	h.runWith(ports, "sort -u", input).expectOutput(t, "spilled sort", "a\nb\nc\n", 0)
	dir := filepath.Join(scratch, ".painted-wolf-sort-spills", "program-1")
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read program spill dir", err)
	if len(entries) != 0 {
		t.Fatalf("completed sort left %d runs", len(entries))
	}
	info, err := os.Stat(dir)
	testutil.FailErr(t, "stat program spill dir", err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("spill dir mode %v", info.Mode().Perm())
	}
	run, err := spills.Create(t.Context())
	testutil.FailErr(t, "leftover run", err)
	testutil.FailErr(t, "write leftover", run.WriteRecord([]byte("x")))
	testutil.FailErr(t, "close program spills", spills.Close())
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("settled program kept its spill directory")
	}
	if _, err := spills.Create(t.Context()); err == nil {
		t.Fatal("closed program spills accepted a run")
	}
	other, err := commandspill.NewSessionSpills(h.tc.Host.SessionScratchDir, "program-2")
	testutil.FailErr(t, "second program", err)
	_, err = other.Create(t.Context())
	testutil.FailErr(t, "second program run", err)
	testutil.FailErr(t, "reclaim after host loss", commandspill.ReclaimSessionSpills(h.tc.Host.SessionScratchDir))
	if _, err := os.Stat(filepath.Join(scratch, ".painted-wolf-sort-spills")); !os.IsNotExist(err) {
		t.Fatal("host loss left session spills")
	}
	for _, id := range []string{"", "../escape", ".hidden"} {
		if _, err := commandspill.NewSessionSpills(h.tc.Host.SessionScratchDir, id); err == nil {
			t.Fatalf("program identity %q accepted", id)
		}
	}
	h.tc.Host.SessionScratchDir = ""
	if _, err := commandspill.NewSessionSpills(h.tc.Host.SessionScratchDir, "program-3"); err == nil {
		t.Fatal("spills without a session TMPDIR accepted")
	}
	testutil.FailErr(t, "reclaim without scratch", commandspill.ReclaimSessionSpills(h.tc.Host.SessionScratchDir))
}
