package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Identities reach these effects from durable plans, so a device number a reboot
// changed must not refuse them, while another entry still must.
func TestGuardedEffectsIgnoreOnlyTheDevice(t *testing.T) {
	seed := func(t *testing.T) (string, string, string) {
		t.Helper()
		root := t.TempDir()
		testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "entry"), []byte("entry"), 0o600))
		testutil.FailErr(t, "seed other", os.WriteFile(filepath.Join(root, "other"), []byte("other"), 0o600))
		entry, err := fspath.EntryIdentity(filepath.Join(root, "entry"))
		testutil.FailErr(t, "identify entry", err)
		other, err := fspath.EntryIdentity(filepath.Join(root, "other"))
		testutil.FailErr(t, "identify other", err)
		return root, "9" + entry, "9" + other
	}
	t.Run("rename", func(t *testing.T) {
		root, entry, other := seed(t)
		if err := RenameGuarded(root, "entry", "moved", other); !errors.Is(err, ErrPostcondition) {
			t.Fatalf("rename with another identity = %v", err)
		}
		testutil.FailErr(t, "rename", RenameGuarded(root, "entry", "moved", entry))
	})
	t.Run("exchange", func(t *testing.T) {
		root, entry, other := seed(t)
		if err := ExchangeGuarded(root, "entry", "other", other, entry); !errors.Is(err, ErrPostcondition) {
			t.Fatalf("exchange with swapped identities = %v", err)
		}
		testutil.FailErr(t, "exchange", ExchangeGuarded(root, "entry", "other", entry, other))
	})
	t.Run("remove tree", func(t *testing.T) {
		root, entry, other := seed(t)
		if err := RemoveTreeGuarded(Location{Root: root, Rel: "entry"}, other); !errors.Is(err, ErrPostcondition) {
			t.Fatalf("remove with another identity = %v", err)
		}
		testutil.FailErr(t, "remove", RemoveTreeGuarded(Location{Root: root, Rel: "entry"}, entry))
	})
}
