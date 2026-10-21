//go:build darwin || linux

package textutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A spilled sort keeps its runs in the program's private directory under the
// session TMPDIR; settling the program and host loss both remove them.
func TestSessionSpillsLiveUnderTheSessionAndAreReclaimed(t *testing.T) {
	h := newHarness(t)
	scratch := t.TempDir()
	h.tc.Host.SessionScratchDir = scratch
	spills, err := NewSessionSpills(h.tc, "program-1")
	testutil.FailErr(t, "session spills", err)
	h.owners.limits["sort_memory_bytes"] = 512
	ports := h.ports()
	ports.Spills = spills
	input := strings.Repeat("b\na\nc\n", 200)
	h.runWith(ports, "sort -u", input).expectOutput(t, "spilled sort", "a\nb\nc\n", 0)
	dir := filepath.Join(scratch, spillRootName, "program-1")
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
	other, err := NewSessionSpills(h.tc, "program-2")
	testutil.FailErr(t, "second program", err)
	_, err = other.Create(t.Context())
	testutil.FailErr(t, "second program run", err)
	testutil.FailErr(t, "reclaim after host loss", ReclaimSessionSpills(h.tc))
	if _, err := os.Stat(filepath.Join(scratch, spillRootName)); !os.IsNotExist(err) {
		t.Fatal("host loss left session spills")
	}
	for _, id := range []string{"", "../escape", ".hidden"} {
		if _, err := NewSessionSpills(h.tc, id); err == nil {
			t.Fatalf("program identity %q accepted", id)
		}
	}
	h.tc.Host.SessionScratchDir = ""
	if _, err := NewSessionSpills(h.tc, "program-3"); err == nil {
		t.Fatal("spills without a session TMPDIR accepted")
	}
	testutil.FailErr(t, "reclaim without scratch", ReclaimSessionSpills(h.tc))
}
