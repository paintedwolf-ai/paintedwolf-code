package layeredfs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Durability belongs to the checkpoint barrier: no mutation and no client sync flushes storage, and
// the barrier's own flushes do not grow with the number of files changed.
func TestFlushesPerOperation(t *testing.T) {
	core, trunk := newCountingCore(t, Config{})
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "lower"), []byte("lower"), 0o644))
	q := func(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }
	flushes := func(step string, op func() error) int64 {
		t.Helper()
		before := core.Stats().Flushes
		testutil.FailErr(t, step, op())
		return core.Stats().Flushes - before
	}
	none := func(step string, op func() error) {
		t.Helper()
		if got := flushes(step, op); got != 0 {
			t.Fatalf("%s flushed %d times, want none", step, got)
		}
	}
	var h HandleID
	none("create", func() (err error) {
		h, _, err = core.CreateFile(q("new"), 0o644, true)
		return err
	})
	none("write", func() error {
		_, err := core.WriteHandle(h, []byte("payload"), 0)
		return err
	})
	none("client sync", func() error { return core.SyncHandle(h) })
	none("close", func() error { return core.CloseHandle(h) })
	none("read", func() error {
		_, err := core.ReadFile(q("new"))
		return err
	})
	none("unlink", func() error { return core.Unlink(q("new"), false) })
	none("first write to a lower file", func() error {
		hid, _, err := core.Open(q("lower"), OpenReadWrite, 0)
		if err != nil {
			return err
		}
		return core.CloseHandle(hid)
	})
	none("mkdir", func() error {
		_, err := core.Mkdir(q("dir"), 0o755)
		return err
	})

	barrierFlushes := func() int64 {
		t.Helper()
		return flushes("journal snapshot", func() error {
			_, err := core.SnapshotJournal()
			return err
		})
	}
	few := barrierFlushes()
	for i := range 50 {
		_, err := core.WriteFile(q(fmt.Sprintf("dir/f%02d", i)), []byte("x"), 0o644)
		testutil.FailErr(t, "write", err)
	}
	if many := barrierFlushes(); many != few {
		t.Fatalf("journal snapshot flushes grew from %d to %d with the change count", few, many)
	}
}
