package delegations

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"golang.org/x/sys/unix"
)

func newRealEngineHost() realEngineHost {
	return realEngineHost{
		adapter:      fuselinux.New(fuselinux.Config{}),
		quiesce:      fuselinux.NoopQuiesceHook,
		flushClients: fuselinux.NoWritableMappingsFlushHook,
		requireAvailable: func(t *testing.T) {
			if _, err := os.Stat("/dev/fuse"); err != nil {
				t.Skip("no /dev/fuse on this host")
			}
		},
		requireServerGone: func(t *testing.T, mountPath string) {
			var st unix.Statfs_t
			if err := unix.Statfs(mountPath, &st); !errors.Is(err, unix.ENOTCONN) {
				t.Fatalf("dead mount statfs: %v", err)
			}
		},
		detach: func(mountPath string) { _ = exec.Command("fusermount3", "-u", "-z", "--", mountPath).Run() },
	}
}
