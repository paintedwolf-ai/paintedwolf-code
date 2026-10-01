package extpacks

import (
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const envLockChildDir = "LYCAON_TEST_LOCK_CHILD_CONFIGDIR"
const envCacheLockChildDir = "LYCAON_TEST_CACHE_LOCK_CHILD_CONFIGDIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(envCacheLockChildDir); dir != "" {
		os.Exit(runCacheLockChild(dir))
	}
	if dir := os.Getenv(envLockChildDir); dir != "" {
		os.Exit(runLockChild(dir))
	}
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func runCacheLockChild(configDir string) int {
	_ = os.Setenv("LYCAON_CONFIG_DIR", configDir)
	release, err := AcquireCacheMutationLock()
	if err == nil {
		release()
		return 1
	}
	if !strings.Contains(err.Error(), "held by another process") {
		return 2
	}
	return 0
}

// runLockChild verifies that a second process reaches the lock timeout.
func runLockChild(configDir string) int {
	_ = os.Setenv("LYCAON_CONFIG_DIR", configDir)
	release, err := AcquireIntentLocks(nil)
	if err == nil {
		release()
		return 1
	}
	if !strings.Contains(err.Error(), "held by another process") {
		return 2
	}
	return 0
}

func TestAcquireIntentLocksExcludesOtherProcesses(t *testing.T) {
	testutil.SkipIfShort(t, "waits for the cross-process intent-lock timeout")
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)

	release, err := AcquireIntentLocks(nil)
	testutil.FailErr(t, "acquire intent locks", err)
	defer release()

	child := exec.Command(os.Args[0], "-test.run", "TestAcquireIntentLocksExcludesOtherProcesses")
	child.Env = append(os.Environ(), envLockChildDir+"="+configDir)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child did not observe the held lock (output %q): %v", out, err)
	}
}

func TestAcquireCacheMutationLockExcludesOtherProcesses(t *testing.T) {
	testutil.SkipIfShort(t, "waits for the cross-process cache-lock timeout")
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)

	release, err := AcquireCacheMutationLock()
	testutil.FailErr(t, "acquire cache lock", err)
	defer release()

	child := exec.Command(os.Args[0], "-test.run", "TestAcquireCacheMutationLockExcludesOtherProcesses")
	child.Env = append(os.Environ(), envCacheLockChildDir+"="+configDir)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child did not observe the held cache lock (output %q): %v", out, err)
	}
}

func TestIntentLockFilesLiveInConfigDirNotManagedContent(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	projectDir := t.TempDir()

	for _, target := range []string{ProjectDesiredPath(projectDir), mustDeviceDesiredPath(t)} {
		lockPath, err := lockFilePathFor(target)
		testutil.FailErr(t, "lock file path", err)
		if strings.HasPrefix(lockPath, projectDir) {
			t.Fatalf("lock file %s lives inside the project", lockPath)
		}
		if !strings.HasPrefix(lockPath, configDir) {
			t.Fatalf("lock file %s is outside the config dir %s", lockPath, configDir)
		}
	}
}

func mustDeviceDesiredPath(t *testing.T) string {
	t.Helper()
	path, err := DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	return path
}
