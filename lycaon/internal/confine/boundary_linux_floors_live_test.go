//go:build linux

package confine_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// floorsScriptEnv carries the operations the floors probe runs, one per line:
// an operation and its paths, relative to the probe's working directory.
const floorsScriptEnv = "PW_CONFINE_FLOORS_SCRIPT"

// floorsProbe runs each scripted operation inside the sandbox and prints
// "<line>: <result>".
func floorsProbe() int {
	scanner := bufio.NewScanner(strings.NewReader(os.Getenv(floorsScriptEnv)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		fmt.Printf("%s: %s\n", line, floorsResult(floorsOp(fields[0], fields[1:])))
	}
	return 0
}

func floorsResult(err error) string {
	if err == nil {
		return "ok"
	}
	var errno unix.Errno
	if errors.As(err, &errno) {
		return unix.ErrnoName(errno)
	}
	return err.Error()
}

func floorsOp(op string, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	openClose := func(path string, flags int) error {
		fd, err := unix.Open(path, flags|unix.O_CLOEXEC, 0o644)
		if err == nil {
			_ = unix.Close(fd)
		}
		return err
	}
	switch op {
	case "create":
		fd, err := unix.Open(arg(0), unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, 0o644)
		if err == nil {
			_, err = unix.Write(fd, []byte("written inside\n"))
			_ = unix.Close(fd)
		}
		return err
	case "write":
		return openClose(arg(0), unix.O_WRONLY|unix.O_TRUNC)
	case "openat2":
		fd, err := unix.Openat2(unix.AT_FDCWD, arg(0), &unix.OpenHow{Flags: unix.O_CREAT | unix.O_WRONLY | unix.O_CLOEXEC, Mode: 0o644})
		if err == nil {
			_ = unix.Close(fd)
		}
		return err
	case "openat2write":
		fd, err := unix.Openat2(unix.AT_FDCWD, arg(0), &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CLOEXEC})
		if err == nil {
			_ = unix.Close(fd)
		}
		return err
	case "rdtrunc":
		return openClose(arg(0), unix.O_RDONLY|unix.O_TRUNC)
	case "truncate":
		return unix.Truncate(arg(0), 0)
	case "unlink":
		return unix.Unlink(arg(0))
	case "linkfollow":
		return unix.Linkat(unix.AT_FDCWD, arg(0), unix.AT_FDCWD, arg(1), unix.AT_SYMLINK_FOLLOW)
	case "linkempty":
		fd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		return unix.Linkat(fd, "", unix.AT_FDCWD, arg(1), unix.AT_EMPTY_PATH)
	case "procfd-write":
		fd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		return openClose("/proc/self/fd/"+strconv.Itoa(fd), unix.O_WRONLY)
	case "mmap-write":
		fd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		data, err := unix.Mmap(fd, 0, 4, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err == nil {
			_ = unix.Munmap(data)
		}
		return err
	case "dirfd-write":
		dfd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(dfd) }()
		fd, err := unix.Openat(dfd, arg(1), unix.O_WRONLY|unix.O_CLOEXEC, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		return err
	case "dirfd-unlink":
		dfd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(dfd) }()
		return unix.Unlinkat(dfd, arg(1), 0)
	case "dirfd-rename":
		dfd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(dfd) }()
		return unix.Renameat2(dfd, arg(1), dfd, arg(2), 0)
	case "dirfd-link":
		dfd, err := unix.Open(arg(0), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(dfd) }()
		return unix.Linkat(dfd, arg(1), dfd, arg(2), 0)
	case "mkdir":
		return unix.Mkdir(arg(0), 0o755)
	case "mkfifo":
		return unix.Mkfifo(arg(0), 0o644)
	case "symlink":
		return unix.Symlink(arg(0), arg(1))
	case "link":
		return unix.Link(arg(0), arg(1))
	case "rename":
		return unix.Rename(arg(0), arg(1))
	case "exchange":
		return unix.Renameat2(unix.AT_FDCWD, arg(0), unix.AT_FDCWD, arg(1), unix.RENAME_EXCHANGE)
	case "tmpfile":
		// An unnamed file gets its name only when it is linked in.
		fd, err := unix.Open(arg(0), unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		return unix.Linkat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), unix.AT_FDCWD, arg(1), unix.AT_SYMLINK_FOLLOW)
	case "read":
		raw, err := os.ReadFile(arg(0))
		if err == nil && !strings.Contains(string(raw), arg(1)) {
			return fmt.Errorf("content %q", raw)
		}
		return unwrapErrno(err)
	case "bulk":
		// Create many files, as a build or package install does.
		count, err := strconv.Atoi(arg(1))
		for i := 0; err == nil && i < count; i++ {
			err = openClose(filepath.Join(arg(0), "f"+strconv.Itoa(i)), unix.O_CREAT|unix.O_WRONLY|unix.O_TRUNC)
		}
		return err
	case "ready":
		// The boundary is applied; the test may now change the host.
		return nil
	case "waitread":
		// Wait for a path another process creates after launch, then read it.
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Lstat(arg(0)); err == nil {
				_, err := os.ReadFile(arg(0))
				return unwrapErrno(err)
			}
		}
		return fmt.Errorf("never appeared")
	}
	return fmt.Errorf("unknown operation %s", op)
}

func unwrapErrno(err error) error {
	var errno unix.Errno
	if err != nil && errors.As(err, &errno) {
		return errno
	}
	return err
}

// floorsRun runs script under c from dir and returns each line's result.
func floorsRun(t *testing.T, c confine.Confinement, dir string, script ...string) map[string]string {
	t.Helper()
	self := requireLinuxSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = append(probeEnv("floors"), floorsScriptEnv+"="+strings.Join(script, "\n"))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("floors probe: %v\n%s", err, out)
	}
	results := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.LastIndex(line, ": "); i > 0 {
			results[line[:i]] = line[i+2:]
		}
	}
	return results
}

// floorStep is one scripted operation and the result it must give.
type floorStep struct{ line, want string }

// expectFloorTable runs the steps in order under c from dir and checks each.
func expectFloorTable(t *testing.T, c confine.Confinement, dir string, steps []floorStep) {
	t.Helper()
	script := make([]string, len(steps))
	for i, step := range steps {
		script[i] = step.line
	}
	results := floorsRun(t, c, dir, script...)
	for _, step := range steps {
		if results[step.line] != step.want {
			t.Errorf("%s: got %q, want %q", step.line, results[step.line], step.want)
		}
	}
}

// A protected location that does not exist at launch stays protected: no
// spelling of a create, link, symlink, or move produces it, while ordinary
// writes beside it keep working.
func TestLinuxAbsentProtectedPathsCannotBeCreated(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "fixture tree", os.MkdirAll(filepath.Join(root, "deep", "er"), 0o755))
	expectFloorTable(t, denyConfinement(root), root, []floorStep{
		{"mkdir .paintedwolf", "ok"},
		{"create .paintedwolf/approvals.yaml", "EACCES"},
		{"create .paintedwolf/mcp.yaml", "EACCES"},
		{"openat2 .paintedwolf/mcp.yaml", "EACCES"},
		{"mkdir .paintedwolf/rules", "EACCES"},
		{"mkdir .paintedwolf/skills", "EACCES"},
		{"create .paintedwolf/notes.txt", "ok"},
		{"mkdir .agents", "ok"},
		{"mkdir .agents/skills", "EACCES"},
		{"create AGENTS.md", "EACCES"},
		{"create deep/er/AGENTS.md", "EACCES"},
		{"create deep/er/agents.md", "EACCES"},
		{"openat2 deep/AGENTS.md", "EACCES"},
		{"mkfifo deep/AGENTS.md", "EACCES"},
		{"mkdir deep/AGENTS.md", "EACCES"},
		{"symlink elsewhere deep/AGENTS.md", "EACCES"},
		{"symlink AGENTS.md deep/pointer", "ok"},
		{"create deep/pointer", "EACCES"},
		{"create notes.txt", "ok"},
		{"link notes.txt deep/er/AGENTS.md", "EACCES"},
		{"create staged.yaml", "ok"},
		{"rename staged.yaml .paintedwolf/mcp.yaml", "EACCES"},
		{"mkdir stage", "ok"},
		{"create stage/mcp.yaml", "ok"},
		{"rename stage .agents", "EACCES"},
		{"tmpfile deep deep/AGENTS.md", "EACCES"},
		{"mkdir .PaintedWolf", "ok"},
		{"create .PaintedWolf/MCP.yaml", "EACCES"},
		{"create src.go", "ok"},
		{"read src.go inside", "ok"},
		{"mkdir newdir", "ok"},
		{"create newdir/file.txt", "ok"},
		{"rename src.go renamed.go", "ok"},
		{"symlink renamed.go alias.go", "ok"},
		{"link renamed.go hard.go", "ok"},
		{"tmpfile deep deep/linked.txt", "ok"},
		{"rename stage stage2", "ok"},
	})
	for _, absent := range []string{".paintedwolf/mcp.yaml", ".paintedwolf/approvals.yaml", ".agents/skills", "AGENTS.md", "deep/er/AGENTS.md", "deep/AGENTS.md", ".PaintedWolf/MCP.yaml"} {
		if _, err := os.Lstat(filepath.Join(root, absent)); err == nil {
			t.Errorf("%s exists on the host after the command", absent)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "deep", "linked.txt")); err != nil || len(raw) != 0 {
		t.Errorf("linked temporary file: %q, %v", raw, err)
	}
}

// An existing protected entry keeps its mount. Moving a directory that holds
// one carries the mask along, and the vacated path cannot be recreated.
func TestLinuxExistingProtectedPathsSurviveParentRename(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"AGENTS.md":             "root instructions",
		"sub/AGENTS.md":         "sub instructions",
		".paintedwolf/mcp.yaml": "servers: {}",
	} {
		full := filepath.Join(root, path)
		testutil.FailErr(t, "fixture dir", os.MkdirAll(filepath.Dir(full), 0o755))
		testutil.FailErr(t, "fixture "+path, os.WriteFile(full, []byte(content), 0o644))
	}
	expectFloorTable(t, denyConfinement(root), root, []floorStep{
		{"write AGENTS.md", "EROFS"},
		{"create AGENTS.md", "EROFS"},
		{"rename AGENTS.md moved.md", "EROFS"},
		{"rename .paintedwolf/mcp.yaml mcp.yaml", "EROFS"},
		{"rename sub sub2", "ok"},
		{"write sub2/AGENTS.md", "EROFS"},
		{"read sub2/AGENTS.md sub", "ok"},
		{"mkdir sub", "ok"},
		{"create sub/AGENTS.md", "EACCES"},
		{"rename .paintedwolf pw-moved", "ok"},
		{"mkdir .paintedwolf", "ok"},
		{"create .paintedwolf/mcp.yaml", "EACCES"},
		{"exchange sub2 sub", "EACCES"},
	})
	for path, content := range map[string]string{"AGENTS.md": "root instructions", "sub2/AGENTS.md": "sub instructions", "pw-moved/mcp.yaml": "servers: {}"} {
		if raw, err := os.ReadFile(filepath.Join(root, path)); err != nil || string(raw) != content {
			t.Errorf("%s after the command: %q, %v", path, raw, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "sub", "AGENTS.md")); err == nil {
		t.Error("the vacated instruction path was recreated")
	}
}

// Approved protected writes still create their exact paths, and a directory
// grant admits files inside it; the rest of the floor holds.
func TestLinuxApprovedProtectedWritesCreateAbsentPaths(t *testing.T) {
	root := t.TempDir()
	c := denyConfinement(root)
	grants, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{
		confine.NewProtectedPathGrant(filepath.Join(root, "AGENTS.md")),
		confine.NewProtectedPathGrant(filepath.Join(root, ".paintedwolf", "rules") + string(filepath.Separator)),
	}, []string{root})
	testutil.FailErr(t, "validate grants", err)
	c.PolicyWriteGrants = grants
	expectFloorTable(t, c, root, []floorStep{
		{"create AGENTS.md", "ok"},
		{"mkdir .paintedwolf", "ok"},
		{"mkdir .paintedwolf/rules", "ok"},
		{"create .paintedwolf/rules/team.yaml", "ok"},
		{"create .paintedwolf/mcp.yaml", "EACCES"},
		{"mkdir deep", "ok"},
		{"create deep/AGENTS.md", "EACCES"},
	})
}

// A read-denied path that does not exist at launch, created by another
// process while the command runs, is unreadable inside the sandbox.
func TestLinuxAbsentReadDeniedPathCreatedDuringTheCommandIsUnreadable(t *testing.T) {
	self := requireLinuxSandbox(t)
	// The account's own home directory lies outside every write root, so only
	// the read floor stands in the way; the test environment's HOME may not.
	account, err := user.Current()
	testutil.FailErr(t, "account", err)
	base, err := os.MkdirTemp(account.HomeDir, ".pw-confine-absent-read-") //nolint:usetesting // Must lie outside standard temporary write roots.
	testutil.FailErr(t, "fixture base", err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	store := filepath.Join(base, "store")
	secret := filepath.Join(store, "credentials")
	beside := filepath.Join(base, "beside.txt")
	testutil.FailErr(t, "fixture beside", os.WriteFile(beside, []byte("readable"), 0o600))
	c := denyConfinement(t.TempDir())
	c.ReadDenyPaths = []string{store}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = append(probeEnv("floors"), floorsScriptEnv+"="+strings.Join([]string{"ready", "read " + beside + " readable", "waitread " + secret}, "\n"))
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "confined reader output", err)
	testutil.FailErr(t, "start confined reader", cmd.Start())
	lines := bufio.NewScanner(stdout)
	var out []string
	for lines.Scan() {
		out = append(out, lines.Text())
		if lines.Text() == "ready: ok" {
			// The boundary was built while the store did not exist.
			testutil.FailErr(t, "create the denied store after launch", os.MkdirAll(store, 0o700))
			testutil.FailErr(t, "write the secret after launch", os.WriteFile(secret, []byte("secret"), 0o600))
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("confined reader: %v\n%s", err, strings.Join(out, "\n"))
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "read "+beside+" readable: ok") || !strings.Contains(joined, "waitread "+secret+": EACCES") {
		t.Fatalf("absent read floor:\n%s", joined)
	}
}

// Late live-trunk AGENTS.md creations and renames (not bind-mounted read-only
// at launch) remain readable inside the installed sandbox while every write,
// openat2 write, truncate, unlink, rename, relative-dirfd mutation, symlink
// alias, hardlink alias, /proc/self/fd descriptor escalation, and writable
// shared mmap is refused at enforcement time, and routine project writes run
// at full speed.
func TestLinuxLateTrunkPolicyFilesAreProtectedInsideInstalledSandbox(t *testing.T) {
	self := requireLinuxSandbox(t)
	root := t.TempDir()
	for _, dir := range []string{"mounted", "sub", "renamed", "stage", "build"} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	testutil.FailErr(t, "write mounted AGENTS.md", os.WriteFile(filepath.Join(root, "mounted", "AGENTS.md"), []byte("mounted-policy"), 0o644))
	testutil.FailErr(t, "write draft.txt", os.WriteFile(filepath.Join(root, "renamed", "draft.txt"), []byte("renamed-policy"), 0o644))

	c := denyConfinement(root)
	c.View = confine.ViewBoundary{
		ReopenedPaths: []string{root},
		Roots: []confine.ViewRootBoundary{{
			RootID:        "proj",
			WorkspacePath: root,
			SourcePath:    root,
			Inventory: confine.PolicyFileInventory{
				Files:      []string{"mounted/AGENTS.md"},
				Complete:   false,
				Generation: 1,
				ReadDirs:   0,
			},
		}},
	}

	steps := []floorStep{
		{"ready", "ok"},
		{"waitread sub/AGENTS.md", "ok"},
		{"waitread renamed/AGENTS.md", "ok"},
		{"read sub/AGENTS.md live-policy", "ok"},
		{"read renamed/AGENTS.md renamed-policy", "ok"},
		// Direct writes, openat2 writes, truncates, unlinks, and renames on late trunk policy files.
		{"write sub/AGENTS.md", "EACCES"},
		{"create sub/AGENTS.md", "EACCES"},
		{"create sub/agents.md", "EACCES"},
		{"openat2 sub/AGENTS.md", "EACCES"},
		{"openat2write sub/AGENTS.md", "EACCES"},
		{"rdtrunc sub/AGENTS.md", "EACCES"},
		{"truncate sub/AGENTS.md", "EACCES"},
		{"unlink sub/AGENTS.md", "EACCES"},
		{"rename sub/AGENTS.md sub/moved.md", "EACCES"},
		{"write renamed/AGENTS.md", "EACCES"},
		{"unlink renamed/AGENTS.md", "EACCES"},
		// Relative dirfd paths.
		{"dirfd-write sub AGENTS.md", "EACCES"},
		{"dirfd-unlink sub AGENTS.md", "EACCES"},
		{"dirfd-rename sub AGENTS.md other.md", "EACCES"},
		{"dirfd-link sub AGENTS.md hard.md", "EACCES"},
		// O_RDONLY descriptor escalation via /proc/self/fd and writable shared mmap.
		{"procfd-write sub/AGENTS.md", "EACCES"},
		{"mmap-write sub/AGENTS.md", "EACCES"},
		// Aliases: symlinks and hardlinks (direct, AT_SYMLINK_FOLLOW, and AT_EMPTY_PATH).
		{"symlink AGENTS.md sub/alias.md", "ok"},
		{"write sub/alias.md", "EACCES"},
		{"openat2write sub/alias.md", "EACCES"},
		{"truncate sub/alias.md", "EACCES"},
		{"link sub/AGENTS.md sub/hard.md", "EACCES"},
		{"linkfollow sub/alias.md sub/hard.md", "EACCES"},
		{"linkempty sub/AGENTS.md sub/hard.md", "EACCES"},
		// Directory RENAME_EXCHANGE on a directory holding a late trunk policy file.
		{"exchange sub stage", "EACCES"},
		// Existing pre-bound floors and absent control-plane floors remain intact.
		{"write mounted/AGENTS.md", "EROFS"},
		{"mkdir .paintedwolf", "ok"},
		{"create .paintedwolf/mcp.yaml", "EACCES"},
		// Ordinary project tool operations and bulk throughput remain unaffected.
		{"create sub/main.go", "ok"},
		{"write sub/main.go", "ok"},
		{"truncate sub/main.go", "ok"},
		{"rename sub/main.go sub/app.go", "ok"},
		{"unlink sub/app.go", "ok"},
		{"bulk build 200", "ok"},
	}
	script := make([]string, len(steps))
	for i, s := range steps {
		script[i] = s.line
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = append(probeEnv("floors"), floorsScriptEnv+"="+strings.Join(script, "\n"))
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "stdout pipe", err)
	testutil.FailErr(t, "start confined probe", cmd.Start())

	results := map[string]string{}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.LastIndex(line, ": "); i > 0 {
			results[line[:i]] = line[i+2:]
		}
		if line == "ready: ok" {
			// Simulate live-trunk policy file creation and rename after sandbox launch.
			testutil.FailErr(t, "create late sub/AGENTS.md", os.WriteFile(filepath.Join(root, "sub", "AGENTS.md"), []byte("live-policy"), 0o644))
			testutil.FailErr(t, "rename draft -> renamed/AGENTS.md", os.Rename(
				filepath.Join(root, "renamed", "draft.txt"),
				filepath.Join(root, "renamed", "AGENTS.md"),
			))
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("confined probe failed: %v", err)
	}
	for _, step := range steps {
		if results[step.line] != step.want {
			t.Errorf("%s: got %q, want %q", step.line, results[step.line], step.want)
		}
	}
	for path, want := range map[string]string{
		"mounted/AGENTS.md": "mounted-policy",
		"sub/AGENTS.md":     "live-policy",
		"renamed/AGENTS.md": "renamed-policy",
	} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(raw) != want {
			t.Errorf("%s after probe = %q (%v), want %q", path, raw, err, want)
		}
	}
}
