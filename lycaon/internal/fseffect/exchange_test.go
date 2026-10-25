package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExchangeGuardedPreservesBothNamesAndIdentities(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"live": "original", "candidate": "replacement"} {
		testutil.FailErr(t, "seed exchange entry", os.WriteFile(filepath.Join(root, name), []byte(body), 0600))
	}
	first, err := fspath.EntryIdentity(filepath.Join(root, "live"))
	testutil.FailErr(t, "identify original", err)
	second, err := fspath.EntryIdentity(filepath.Join(root, "candidate"))
	testutil.FailErr(t, "identify candidate", err)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if err := ExchangeGuarded(root, "live", "candidate", first, second); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported exchange = %v", err)
		}
		return
	}
	if err := ExchangeGuarded(root, "live", "candidate", "wrong", second); !errors.Is(err, ErrPostcondition) {
		t.Fatalf("identity refusal = %v", err)
	}
	for i := 0; i < 2; i++ {
		testutil.FailErr(t, "exchange guarded entries", ExchangeGuarded(root, "live", "candidate", first, second))
		first, second = second, first
		for name, want := range map[string]string{"live": first, "candidate": second} {
			got, err := fspath.EntryIdentity(filepath.Join(root, name))
			testutil.FailErr(t, "inspect continuous entry", err)
			if got != want {
				t.Fatalf("%s identity changed", name)
			}
		}
	}
	testutil.FailErr(t, "seed linked parent", os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
	if err := ExchangeGuarded(root, "live", "outside/candidate", first, second); err == nil {
		t.Fatal("exchange traversed linked parent")
	}
}
