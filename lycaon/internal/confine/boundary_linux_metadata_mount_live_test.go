//go:build linux

package confine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const metadataReadOnlySourceEnv = "PW_METADATA_READ_ONLY_SOURCE"

func TestLinuxMetadataPreservesReadOnlySourceMount(t *testing.T) {
	if root := os.Getenv(metadataReadOnlySourceEnv); root != "" {
		testutil.FailErr(t, "isolate source mount propagation", unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""))
		testutil.FailErr(t, "bind source read-only fixture", unix.Mount(root, root, "", unix.MS_BIND, ""))
		testutil.FailErr(t, "seal source fixture mount", unix.MountSetattr(unix.AT_FDCWD, root, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}))
		got := runMetadata(t, denyConfinement(root), []string{"restricted-mount " + filepath.Join(root, "file"), "chmod " + filepath.Join(root, "file"), "fchmodat-empty " + filepath.Join(root, "file")})
		if got["restricted-mount "+filepath.Join(root, "file")] != "ok" {
			t.Fatalf("child lost source mount restrictions: %v", got)
		}
		for _, op := range []string{"chmod", "fchmodat-empty"} {
			if got[op+" "+filepath.Join(root, "file")] != "EROFS" {
				t.Fatalf("a write grant changed original read-only mount: %v", got)
			}
		}
		var st unix.Statfs_t
		testutil.FailErr(t, "inspect original source mount flags", unix.Statfs(root, &st))
		want := int64(unix.ST_RDONLY | unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV)
		if st.Flags&want != want {
			t.Fatal("source mount restrictions were cleared")
		}
		return
	}
	requireLinuxSandbox(t)
	root := t.TempDir()
	seedMetadataFile(t, filepath.Join(root, "file"))
	self, err := os.Executable()
	testutil.FailErr(t, "locate source mount fixture binary", err)
	cmd := exec.Command(self, "-test.run=^TestLinuxMetadataPreservesReadOnlySourceMount$")
	cmd.Env = append(os.Environ(), metadataReadOnlySourceEnv+"="+root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source mount fixture failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("source mount fixture did not finish: %s", out)
	}
}
