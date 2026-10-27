//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExchangeReportsSyncFailureWithoutLosingEitherFile(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			root := t.TempDir()
			testutil.FailErr(t, "create candidate directory", os.Mkdir(filepath.Join(root, "work"), 0700))
			for name, body := range map[string]string{"live": "original", "work/candidate": "replacement"} {
				testutil.FailErr(t, "seed exchange entry", os.WriteFile(filepath.Join(root, name), []byte(body), 0600))
			}
			first, err := fspath.EntryIdentity(filepath.Join(root, "live"))
			testutil.FailErr(t, "identify original", err)
			second, err := fspath.EntryIdentity(filepath.Join(root, "work/candidate"))
			testutil.FailErr(t, "identify replacement", err)
			from, err := openParent(Location{Root: root, Rel: "live"}, false, 0)
			testutil.FailErr(t, "hold live parent", err)
			defer from.close()
			to, err := openParent(Location{Root: root, Rel: "work/candidate"}, false, 0)
			testutil.FailErr(t, "hold candidate parent", err)
			defer to.close()
			calls := 0
			err = exchangeParents(from, to, func(fd int) error {
				calls++
				if calls == failAt {
					return syscall.EIO
				}
				return fsyncFD(fd)
			})
			if !errors.Is(err, syscall.EIO) || calls != 2 {
				t.Fatalf("sync error=%v calls=%d", err, calls)
			}
			for name, want := range map[string]string{"live": second, "work/candidate": first} {
				got, err := fspath.EntryIdentity(filepath.Join(root, name))
				testutil.FailErr(t, "locate file after sync failure", err)
				if got != want {
					t.Fatalf("%s lost exchanged identity", name)
				}
			}
		})
	}
}
