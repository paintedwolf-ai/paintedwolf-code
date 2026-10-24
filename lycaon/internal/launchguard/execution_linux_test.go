//go:build linux

package launchguard

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Run in a separate process because Landlock applies irreversibly to all
// threads. Root owns the closure and can write it, so DAC cannot mask a
// missing Landlock prohibition on gaining EXECUTE by reparenting.
func TestExecutionRestrictionPreservesDataReparenting(t *testing.T) {
	const childEnv = "PW_LAUNCH_EXECUTION_TEST"
	if os.Getenv(childEnv) != "1" {
		blobFixture := filepath.Join(t.TempDir(), "blob-commit")
		goBuild(t, blobFixture, "./testdata/blobcommit", "")
		testutil.FailErr(t, "make blob fixture readable", os.Chmod(filepath.Dir(blobFixture), 0o755))
		out, err := exec.Command("sudo", "-n", "env", childEnv+"=1", "PW_BLOB_FIXTURE="+blobFixture, os.Args[0],
			"-test.run=^TestExecutionRestrictionPreservesDataReparenting$", "-test.v").CombinedOutput()
		if err != nil {
			t.Fatalf("execution restriction child: %v\n%s", err, out)
		}
		return
	}
	base, err := os.MkdirTemp("/opt", "pw-execution-test-") //nolint:usetesting // The root-controlled execution closure must live outside writable temporary roots.
	testutil.FailErr(t, "private execution fixture", err)
	defer func() { testutil.FailErr(t, "remove execution fixture", os.RemoveAll(base)) }()
	closure := filepath.Join(base, "closure")
	from, to := filepath.Join(base, "from"), filepath.Join(base, "to")
	for _, path := range []string{closure, from, to} {
		testutil.FailErr(t, "create execution fixture directory", os.Mkdir(path, 0o755))
	}
	image, err := os.Open("/proc/self/exe")
	testutil.FailErr(t, "open test image", err)
	defer func() { _ = image.Close() }()
	allowed := filepath.Join(closure, "allowed")
	copy, err := os.OpenFile(allowed, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	testutil.FailErr(t, "create closure image", err)
	_, err = io.Copy(copy, image)
	testutil.FailErr(t, "copy closure image", err)
	testutil.FailErr(t, "close closure image", copy.Close())
	blobProgram := filepath.Join(closure, "blob-commit")
	blobInput, err := os.Open(os.Getenv("PW_BLOB_FIXTURE"))
	testutil.FailErr(t, "open blob fixture", err)
	blobOutput, err := os.OpenFile(blobProgram, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	testutil.FailErr(t, "install blob fixture", err)
	_, err = io.Copy(blobOutput, blobInput)
	testutil.FailErr(t, "copy blob fixture", err)
	testutil.FailErr(t, "close blob fixture input", blobInput.Close())
	testutil.FailErr(t, "close blob fixture output", blobOutput.Close())
	source := filepath.Join(from, "source")
	testutil.FailErr(t, "write data", os.WriteFile(source, []byte("data"), 0o755))
	// These same directory pairs permit moves and links before restriction.
	control := filepath.Join(closure, "control")
	testutil.FailErr(t, "control rename into closure", os.Rename(source, control))
	testutil.FailErr(t, "restore control data", os.Rename(control, source))
	testutil.FailErr(t, "control link into closure", os.Link(source, control))
	testutil.FailErr(t, "remove control link", os.Remove(control))
	_, _, errno := syscall.AllThreadsSyscall(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0)
	if errno != 0 {
		t.Fatalf("set no_new_privs: %v", errno)
	}
	testutil.FailErr(t, "restrict execution", restrictExecution(GenerationPolicy{LogicalInstallRoot: closure}, image))

	t.Run("ordinary data operations", func(t *testing.T) {
		destination := filepath.Join(to, "destination")
		testutil.FailErr(t, "cross-directory rename", os.Rename(source, destination))
		testutil.FailErr(t, "cross-directory renameat2 noreplace", unix.Renameat2(unix.AT_FDCWD, destination, unix.AT_FDCWD, source, unix.RENAME_NOREPLACE))
		testutil.FailErr(t, "exchange destination", os.WriteFile(destination, []byte("other"), 0o600))
		testutil.FailErr(t, "cross-directory renameat2 exchange", unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE))
		got, err := os.ReadFile(source)
		testutil.FailErr(t, "read exchanged file", err)
		if string(got) != "other" {
			t.Fatalf("exchange source = %q", got)
		}
		testutil.FailErr(t, "cross-directory link", os.Link(source, filepath.Join(to, "linked")))
	})
	t.Run("execution rights cannot increase", func(t *testing.T) {
		for _, operation := range []struct {
			name string
			run  func(string, string) error
		}{
			{"rename", os.Rename},
			{"link", os.Link},
		} {
			err := operation.run(source, filepath.Join(closure, operation.name))
			if !errors.Is(err, unix.EXDEV) {
				t.Fatalf("%s into executable closure = %v, want EXDEV", operation.name, err)
			}
		}
		if err := exec.Command("/usr/bin/true").Run(); !errors.Is(err, unix.EACCES) {
			t.Fatalf("execution outside closure = %v, want EACCES", err)
		}
		out, err := exec.Command(allowed, "-test.run=^$").CombinedOutput()
		if err != nil {
			t.Fatalf("execution inside closure: %v\n%s", err, out)
		}
	})
	t.Run("engine blob commit", func(t *testing.T) {
		if out, err := exec.Command(blobProgram, base).CombinedOutput(); err != nil {
			t.Fatalf("commit model output blob under execution restriction: %v\n%s", err, out)
		}
	})
}
