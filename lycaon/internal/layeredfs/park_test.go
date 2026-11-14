//go:build darwin || linux

package layeredfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A parked view holds nothing open on its backing's volume; the next operation reopens the backing
// as it was, and refuses one that was replaced.
func TestParkedBackingReopensAsItWasOrRefuses(t *testing.T) {
	t.Parallel()
	core, _, backing := newSingleRootTestCore(t)
	qp := QualifiedPath{RootID: "primary", Rel: "a.txt"}
	_, err := core.WriteFile(qp, []byte("one\n"), 0o644)
	testutil.FailErr(t, "write", err)

	if !core.ParkBacking() {
		t.Fatal("an idle view did not park")
	}
	if core.backing.workDirFile != nil || core.backing.baseDirFile != nil || core.backing.journalDirFile != nil {
		t.Fatal("a parked view still holds its backing directories")
	}
	for _, provider := range core.roots.ownedOS {
		if provider.rootFile != nil {
			t.Fatal("a parked view still holds its trunk root")
		}
	}
	got, err := core.ReadFile(qp)
	testutil.FailErr(t, "read after park", err)
	if string(got) != "one\n" {
		t.Fatalf("read %q after park", got)
	}

	h, _, err := core.CreateFile(QualifiedPath{RootID: "primary", Rel: "held.txt"}, 0o644, true)
	testutil.FailErr(t, "open a handle", err)
	if core.ParkBacking() {
		t.Fatal("a view with an open handle parked")
	}
	testutil.FailErr(t, "close the handle", core.CloseHandle(h))

	if !core.ParkBacking() {
		t.Fatal("the idle view did not park again")
	}
	work := filepath.Join(backing, "working")
	testutil.FailErr(t, "move backing aside", os.Rename(work, work+".moved"))
	testutil.FailErr(t, "replace backing", os.Mkdir(work, 0o700))
	if _, err := core.ReadFile(qp); !errors.Is(err, ErrBackingUnavailable) {
		t.Fatalf("a replaced backing = %v, want ErrBackingUnavailable", err)
	}
}
