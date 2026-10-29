//go:build linux

package confine_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

var metadataOperations = []string{"chmod", "fchmodat", "fchmod", "openat2-fchmod", "open-create-fchmod", "fchmodat-empty", "chown", "lchown", "fchownat", "fchown", "fchownat-empty", "utimes", "utimensat", "lutimensat", "futimes", "utimensat-empty", "setxattr", "lsetxattr", "fsetxattr", "removexattr", "lremovexattr", "fremovexattr", "setflags"}

func seedMetadataFile(t *testing.T, path string) {
	t.Helper()
	testutil.FailErr(t, "create metadata fixture parent", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "seed metadata fixture", os.WriteFile(path, []byte("original"), 0o644))
	testutil.FailErr(t, "seed metadata timestamps", unix.UtimesNano(path, []unix.Timespec{{Sec: 100000000}, {Sec: 100000000}}))
	for _, name := range []string{"user.pw-metadata", "user.pw-remove", "user.pw-remove-link", "user.pw-remove-fd"} {
		testutil.FailErr(t, "seed metadata attribute", unix.Setxattr(path, name, []byte("before"), 0))
	}
}

func runMetadata(t *testing.T, c confine.Confinement, script []string, input *os.File) map[string]string {
	t.Helper()
	self := requireLinuxSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{"-test.run=^TestLinuxMetadataProbeProcess$"}, c)
	testutil.FailErr(t, "prepare metadata command", err)
	defer cleanup()
	cmd.Stdin = input
	cmd.Env = append(os.Environ(), metadataScriptEnv+"="+strings.Join(script, "\n"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("metadata command failed: %v\n%s", err, out)
	}
	results := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.LastIndex(line, ": "); i > 0 {
			results[line[:i]] = line[i+2:]
		}
	}
	return results
}

func TestLinuxMetadataOutsideWriteRootsIsReadOnly(t *testing.T) {
	requireLinuxSandbox(t)
	root := t.TempDir()
	outside := outsideTemporaryWriteRoots(t)
	insideFile, outsideFile := filepath.Join(root, "allowed"), filepath.Join(outside, "outside")
	seedMetadataFile(t, insideFile)
	seedMetadataFile(t, outsideFile)
	var before unix.Stat_t
	testutil.FailErr(t, "stat outside before", unix.Stat(outsideFile, &before))
	outsideFlags := metadataFlags(t, outsideFile)
	script := []string{}
	for _, op := range metadataOperations {
		script = append(script, op+" "+outsideFile, op+" "+insideFile)
	}
	got := runMetadata(t, denyConfinement(root), script, nil)
	for _, op := range metadataOperations {
		for _, tc := range []struct{ path, want string }{{outsideFile, "EROFS"}, {insideFile, "ok"}} {
			if value := got[op+" "+tc.path]; value != tc.want {
				t.Errorf("%s %s = %q, want %s", op, filepath.Base(tc.path), value, tc.want)
			}
		}
	}
	var after unix.Stat_t
	testutil.FailErr(t, "stat outside after", unix.Stat(outsideFile, &after))
	if after.Mode != before.Mode || after.Uid != before.Uid || after.Gid != before.Gid || after.Mtim != before.Mtim || after.Ctim != before.Ctim || metadataFlags(t, outsideFile) != outsideFlags {
		t.Fatal("refused metadata operations changed the outside file")
	}
	for _, name := range []string{"user.pw-metadata", "user.pw-remove", "user.pw-remove-link", "user.pw-remove-fd"} {
		buf := make([]byte, 16)
		n, err := unix.Getxattr(outsideFile, name, buf)
		testutil.FailErr(t, "read original outside attribute", err)
		if string(buf[:n]) != "before" {
			t.Fatalf("outside attribute %s changed", name)
		}
	}
	data, err := os.ReadFile(outsideFile)
	testutil.FailErr(t, "read original outside bytes", err)
	if string(data) != "original" {
		t.Fatal("outside bytes changed")
	}
}

func metadataFlags(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open flags fixture", err)
	defer func() { _ = unix.Close(fd) }()
	flags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	testutil.FailErr(t, "read flags fixture", err)
	return flags
}

func TestLinuxMetadataFollowsTargetsAndExactWriteGrants(t *testing.T) {
	requireLinuxSandbox(t)
	root := t.TempDir()
	outside := outsideTemporaryWriteRoots(t)
	allowed, denied := filepath.Join(root, "allowed"), filepath.Join(outside, "denied")
	mask := filepath.Join(root, "read-only")
	exact, sibling := filepath.Join(mask, "AGENTS.md"), filepath.Join(mask, "neighbor")
	for _, p := range []string{allowed, denied, exact, sibling} {
		seedMetadataFile(t, p)
	}
	outward, inward := filepath.Join(root, "outward"), filepath.Join(outside, "inward")
	testutil.FailErr(t, "create outward symlink", os.Symlink(denied, outward))
	testutil.FailErr(t, "create inward symlink", os.Symlink(allowed, inward))
	c := denyConfinement(root)
	c.ReadRoots = []string{mask}
	c.Roots = append(c.Roots, mask)
	grants, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(exact)}, []string{root})
	testutil.FailErr(t, "validate exact metadata grant", err)
	c.PolicyWriteGrants = grants
	script := []string{}
	for _, op := range []string{"chmod", "fchmod", "fchmodat-empty", "utimensat", "utimensat-empty", "setxattr", "fsetxattr", "setflags"} {
		for _, p := range []string{outward, inward, exact, sibling} {
			script = append(script, op+" "+p)
		}
	}
	got := runMetadata(t, c, script, nil)
	for _, line := range script {
		want := "EROFS"
		if strings.HasSuffix(line, " "+inward) || strings.HasSuffix(line, " "+exact) {
			want = "ok"
		}
		if got[line] != want {
			t.Errorf("%s = %q, want %s", line, got[line], want)
		}
	}
	got = runMetadata(t, c, []string{"lchown " + outward, "lutimensat " + outward, "lchown " + inward, "lutimensat " + inward}, nil)
	for line, value := range got {
		want := "EROFS"
		if strings.HasSuffix(line, " "+outward) {
			want = "ok"
		}
		if value != want {
			t.Errorf("symlink inode %s = %q, want %s", line, value, want)
		}
	}
}

func TestLinuxReadRestorationDoesNotGrantMetadataWrites(t *testing.T) {
	requireLinuxSandbox(t)
	cfg := filepath.Join(outsideTemporaryWriteRoots(t), "control")
	readable := filepath.Join(cfg, "public", "file")
	scratch := filepath.Join(cfg, "sessions", "own", "scratch")
	scratchFile := filepath.Join(scratch, "file")
	seedMetadataFile(t, readable)
	seedMetadataFile(t, scratchFile)
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	c := denyConfinement(t.TempDir())
	c.ReadRoots = []string{filepath.Dir(readable)}
	c.SessionScratchRoot = scratch
	script := []string{}
	for _, op := range []string{"chmod", "fchmodat-empty", "utimensat-empty", "setxattr", "setflags"} {
		script = append(script, op+" "+readable, op+" "+scratchFile)
	}
	got := runMetadata(t, c, script, nil)
	for _, line := range script {
		want := "EROFS"
		if strings.HasSuffix(line, " "+scratchFile) {
			want = "ok"
		}
		if got[line] != want {
			t.Errorf("%s = %q, want %s", line, got[line], want)
		}
	}
}

func TestLinuxInheritedInputMetadata(t *testing.T) {
	requireLinuxSandbox(t)
	outside := filepath.Join(outsideTemporaryWriteRoots(t), "input")
	seedMetadataFile(t, outside)
	payload := strings.Repeat("byte-stream-", 20000)
	testutil.FailErr(t, "seed multiple byte-copy blocks", os.WriteFile(outside, []byte(payload), 0o644))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	// Prime ordinary read atime before testing explicit descriptor mutations.
	_, err := os.ReadFile(outside)
	testutil.FailErr(t, "read input control", err)
	var before unix.Stat_t
	testutil.FailErr(t, "inspect input before", unix.Stat(outside, &before))
	fd, err := unix.Open(outside, unix.O_RDONLY|unix.O_NOATIME|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open owned no-atime read-only input stream", err)
	input := os.NewFile(uintptr(fd), "owned-byte-input")
	defer func() { _ = input.Close() }()
	script := []string{"stdin-pipe input", "fchmod-stdin input", "fchown-stdin input", "futimes-stdin input", "fsetxattr-stdin input", "read-stdin-digest " + digest}
	got := runMetadata(t, denyConfinement(t.TempDir()), script, input)
	if got[script[0]] != "ok" || got[script[5]] != "ok" {
		t.Fatalf("regular input was not delivered as bytes through EOF: %v", got)
	}
	for _, line := range script {
		if _, ok := got[line]; !ok {
			t.Fatalf("descriptor operation did not complete: %s", line)
		}
	}
	var after unix.Stat_t
	testutil.FailErr(t, "inspect input after", unix.Stat(outside, &after))
	if before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Mtim != after.Mtim || before.Ctim != after.Ctim || before.Atim != after.Atim {
		t.Fatalf("byte input changed source metadata: before=%+v after=%+v", before, after)
	}
	buf := make([]byte, 16)
	n, err := unix.Getxattr(outside, "user.pw-metadata", buf)
	testutil.FailErr(t, "read source input xattr", err)
	if string(buf[:n]) != "before" {
		t.Fatal("descriptor operation changed source input xattr")
	}
}

const metadataReadOnlySourceEnv = "PW_METADATA_READ_ONLY_SOURCE"

func TestLinuxMetadataPreservesReadOnlySourceMount(t *testing.T) {
	if root := os.Getenv(metadataReadOnlySourceEnv); root != "" {
		testutil.FailErr(t, "isolate source mount propagation", unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""))
		testutil.FailErr(t, "bind source read-only fixture", unix.Mount(root, root, "", unix.MS_BIND, ""))
		testutil.FailErr(t, "seal source fixture mount", unix.MountSetattr(unix.AT_FDCWD, root, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}))
		got := runMetadata(t, denyConfinement(root), []string{"restricted-mount " + filepath.Join(root, "file"), "chmod " + filepath.Join(root, "file"), "fchmodat-empty " + filepath.Join(root, "file")}, nil)
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
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false, AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN}}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source mount fixture failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("source mount fixture did not finish: %s", out)
	}
}
