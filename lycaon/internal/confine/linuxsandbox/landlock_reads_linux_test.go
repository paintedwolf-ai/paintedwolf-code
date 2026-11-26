//go:build linux

package linuxsandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadRulesHoldAnAbsentDeniedPathByItsWay(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".bashrc", "projects", ".config"} {
		testutil.FailErr(t, "fixture "+name, os.Mkdir(filepath.Join(home, name), 0o700))
	}
	testutil.FailErr(t, "fixture link", os.Symlink("projects", filepath.Join(home, "link")))
	absent := filepath.Join(home, ".config", "gcloud")
	rules, err := readRules(Filesystem{Hidden: []string{absent, filepath.Join(home, "present")}})
	testutil.FailErr(t, "read rules", err)
	byPath := map[string]uint64{}
	for _, r := range rules {
		byPath[r.path] = r.access
	}
	for _, dir := range []string{"/", home, filepath.Join(home, ".config")} {
		if byPath[dir] != unix.LANDLOCK_ACCESS_FS_READ_DIR {
			t.Fatalf("%s on the way grants %#x, want listing only", dir, byPath[dir])
		}
	}
	for _, beside := range []string{filepath.Join(home, ".bashrc"), filepath.Join(home, "projects"), "/usr"} {
		if byPath[beside] != landlockReadExecute {
			t.Fatalf("%s beside the way grants %#x, want read and execute", beside, byPath[beside])
		}
	}
	if _, ok := byPath[absent]; ok {
		t.Fatal("the absent denied path was granted")
	}
	if _, ok := byPath[filepath.Join(home, "link")]; ok {
		t.Fatal("a symlink was ruled; its target is ruled where it lives")
	}
}

func TestReadRulesKeepTheWholeTreeWhenEveryDeniedPathExists(t *testing.T) {
	present := t.TempDir()
	rules, err := readRules(Filesystem{Hidden: []string{present}})
	testutil.FailErr(t, "read rules", err)
	if len(rules) != 1 || rules[0].path != "/" || rules[0].access != landlockReadExecute {
		t.Fatalf("rules = %+v, want read and execute on /", rules)
	}
	// A denied path inside a write root keeps that root's grant.
	root := t.TempDir()
	rules, err = readRules(Filesystem{Hidden: []string{filepath.Join(root, "absent")}, WriteRoots: []string{root}})
	testutil.FailErr(t, "read rules", err)
	if len(rules) != 1 || rules[0].path != "/" {
		t.Fatalf("rules = %+v, want only /", rules)
	}
}

func TestLandlockProbeRefusedSeparatesCreationFromCleanup(t *testing.T) {
	dir := t.TempDir()
	dirFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open probe dir", err)
	probe := landlockProbe{fd: dirFD}
	t.Cleanup(probe.close)

	origOpenat, origUnlinkat := probeOpenat, probeUnlinkat
	t.Cleanup(func() {
		probeOpenat, probeUnlinkat = origOpenat, origUnlinkat
	})

	// Creation succeeds on the real directory fd, while unlink cleanup fails with EACCES:
	// refused() MUST return false (cleanup EACCES must never attest Landlock restriction).
	probeOpenat = origOpenat
	probeUnlinkat = func(dirfd int, path string, flags int) error {
		_ = origUnlinkat(dirfd, path, flags)
		return unix.EACCES
	}
	if probe.refused() {
		t.Fatal("probe.refused() = true when Openat succeeded and only Unlinkat cleanup returned EACCES")
	}

	// Non-EACCES Openat failure must not attest restriction.
	probeOpenat = func(int, string, int, uint32) (int, error) {
		return -1, unix.ENOSPC
	}
	probeUnlinkat = origUnlinkat
	if probe.refused() {
		t.Fatal("probe.refused() = true when Openat failed with ENOSPC")
	}

	// Only EACCES on Openat itself attests Landlock creation denial.
	probeOpenat = func(int, string, int, uint32) (int, error) {
		return -1, unix.EACCES
	}
	if !probe.refused() {
		t.Fatal("probe.refused() = false when Openat failed with EACCES")
	}
}

func TestWriteOnlyRootsExcludeReadAccessWithoutScanningPopulatedTemp(t *testing.T) {
	base := t.TempDir()
	temp := filepath.Join(base, "populated-temp")
	scratch := filepath.Join(temp, "scratch")
	testutil.FailErr(t, "mkdir scratch", os.MkdirAll(scratch, 0o700))
	for i := range 200 {
		testutil.FailErr(t, "seed temp entry", os.WriteFile(filepath.Join(temp, "f-"+strconv.Itoa(i)), []byte("x"), 0o600))
	}
	exact := filepath.Join(temp, "f-0")
	fs := Filesystem{
		SharedTemp:     []string{temp},
		WriteRoots:     []string{temp, scratch},
		WriteOnlyRoots: []string{temp},
		ReadRoots:      []string{scratch},
		ReadFiles:      []string{exact},
	}
	rules, err := landlockRules(fs, 3)
	testutil.FailErr(t, "landlockRules", err)
	byPath := map[string]uint64{}
	for _, r := range rules {
		byPath[r.path] = r.access
		if filepath.Dir(r.path) == temp && r.path != scratch && r.path != exact {
			t.Fatalf("populated write-only temp leaked per-entry rule for %s", r.path)
		}
	}
	if byPath[temp]&(unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR) != 0 {
		t.Fatalf("write-only root %s kept read access %#x", temp, byPath[temp])
	}
	if byPath[temp]&unix.LANDLOCK_ACCESS_FS_MAKE_REG == 0 {
		t.Fatalf("write-only root %s lost write access %#x", temp, byPath[temp])
	}
	if byPath[scratch] != (landlockReadExecute&handledAccesses(3)) && byPath[scratch] != handledAccesses(3) {
		t.Fatalf("nested scratch access = %#x", byPath[scratch])
	}
	if byPath[exact]&unix.LANDLOCK_ACCESS_FS_READ_FILE == 0 {
		t.Fatalf("exact read file %s lost read access %#x", exact, byPath[exact])
	}
}
