//go:build unix

package referenceuserland

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ServeDriver answers one request inside a userland container, whose root is /.
func ServeDriver(in io.Reader, out io.Writer) error {
	return serve("/", in, out)
}

// serve answers a request against the userland rooted at root. Outside a container
// root is a scratch directory, and an unprivileged driver runs programs as itself.
func serve(root string, in io.Reader, out io.Writer) error {
	var req request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	encoder := json.NewEncoder(out)
	if req.Inventory {
		inv, err := inventory(root)
		if err != nil {
			return err
		}
		return encoder.Encode(inv)
	}
	d := &driver{root: root, privileged: os.Geteuid() == 0, scratch: filepath.Join(root, driverScratch)}
	if err := os.MkdirAll(d.scratch, 0o700); err != nil {
		return err
	}
	for i, c := range req.Cases {
		if err := c.validate(); err != nil {
			return fmt.Errorf("case %d: %w", i, err)
		}
		result, err := d.run(req.Shell, c)
		if err != nil {
			return fmt.Errorf("case %d: %w", i, err)
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
	return nil
}

type driver struct {
	root       string
	scratch    string
	privileged bool
}

func (d *driver) path(name string) string {
	return filepath.Join(d.root, name)
}

func (d *driver) run(shell string, c Case) (Result, error) {
	workspace := d.path(workspaceDir)
	if err := d.reset(); err != nil {
		return Result{}, fmt.Errorf("reset: %w", err)
	}
	if err := d.seed(workspace, c.Workspace); err != nil {
		return Result{}, fmt.Errorf("seed: %w", err)
	}
	stdout, err := os.Create(filepath.Join(d.scratch, "stdout"))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(filepath.Join(d.scratch, "stderr"))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = stderr.Close() }()

	cmd := exec.Command(shell, "-c", c.Program)
	cmd.Dir = workspace
	cmd.Env = environment(d.root, shell)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if d.privileged {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: referenceUID, Gid: referenceGID, Groups: []uint32{referenceGID}}
	}
	// The child inherits the umask at start; restoring it keeps a host test process unchanged.
	mask := syscall.Umask(0o022)
	err = cmd.Start()
	syscall.Umask(mask)
	if err != nil {
		return Result{}, fmt.Errorf("start %s: %w", shell, err)
	}
	waitErr := cmd.Wait()
	// Whatever the program left running in its process group ends with it.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	status, err := exitStatus(cmd.ProcessState, waitErr)
	if err != nil {
		return Result{}, err
	}
	result := Result{Status: status}
	if result.Stdout, err = os.ReadFile(stdout.Name()); err != nil {
		return Result{}, err
	}
	if result.Stderr, err = os.ReadFile(stderr.Name()); err != nil {
		return Result{}, err
	}
	if result.Files, err = track(workspace, c.Tracked); err != nil {
		return Result{}, err
	}
	return result, nil
}

// exitStatus reports a status the way a shell does: a signal's number plus 128.
func exitStatus(state *os.ProcessState, waitErr error) (int, error) {
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return 0, waitErr
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return state.ExitCode(), nil
}

// reset empties the workspace, home and temporary directories.
func (d *driver) reset() error {
	for _, dir := range []struct {
		name  string
		mode  fs.FileMode
		owned bool
	}{
		{workspaceDir, 0o755, true},
		{homeDir, 0o755, true},
		{tmpDir, fs.ModeSticky | 0o777, false},
	} {
		full := d.path(dir.name)
		if err := os.RemoveAll(full); err != nil {
			return err
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(full, dir.mode); err != nil {
			return err
		}
		if dir.owned {
			if err := d.own(full); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *driver) own(name string) error {
	if !d.privileged {
		return nil
	}
	return os.Lchown(name, referenceUID, referenceGID)
}

// seed writes the entries, then applies permissions deepest first, so a read-only
// directory is still writable while its children are created, and finally sets
// every seeded time.
func (d *driver) seed(workspace string, entries map[string]Entry) error {
	created := []string{workspace}
	perms := map[string]fs.FileMode{}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry := entries[name]
		full := filepath.Join(workspace, filepath.FromSlash(name))
		parents, err := d.mkdirs(workspace, filepath.Dir(full))
		if err != nil {
			return err
		}
		created = append(created, parents...)
		switch entry.Mode.Type() {
		case fs.ModeSymlink:
			err = os.Symlink(entry.Link, full)
		case fs.ModeDir:
			if err = os.Mkdir(full, 0o755); errors.Is(err, fs.ErrExist) {
				err = nil
			}
			perms[full] = permissions(entry.Mode, 0o755)
		default:
			err = os.WriteFile(full, entry.Data, 0o600)
			perms[full] = permissions(entry.Mode, 0o644)
		}
		if err != nil {
			return err
		}
		if err := d.own(full); err != nil {
			return err
		}
		created = append(created, full)
	}
	for _, name := range slices.Backward(slices.Sorted(maps.Keys(perms))) {
		if err := os.Chmod(name, perms[name]); err != nil {
			return err
		}
	}
	at := unix.NsecToTimespec(SeedTime.UnixNano())
	for _, name := range created {
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, name, []unix.Timespec{at, at}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("set time of %s: %w", name, err)
		}
	}
	return nil
}

// mkdirs creates the missing directories from workspace down to dir and returns them.
func (d *driver) mkdirs(workspace, dir string) ([]string, error) {
	rel, err := filepath.Rel(workspace, dir)
	if err != nil || rel == "." {
		return nil, err
	}
	var created []string
	current := workspace
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		err := os.Mkdir(current, 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := d.own(current); err != nil {
			return nil, err
		}
		created = append(created, current)
	}
	return created, nil
}

func permissions(mode, fallback fs.FileMode) fs.FileMode {
	special := mode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	if mode.Perm() == 0 {
		return fallback | special
	}
	return mode.Perm() | special
}

func track(workspace string, names []string) (map[string]*Entry, error) {
	files := make(map[string]*Entry, len(names))
	for _, name := range names {
		full := filepath.Join(workspace, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			files[name] = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		entry := &Entry{Mode: info.Mode() & (fs.ModeType | fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)}
		switch {
		case info.Mode().Type() == fs.ModeSymlink:
			entry.Link, err = os.Readlink(full)
		case info.Mode().IsRegular():
			entry.Data, err = os.ReadFile(full)
		}
		if err != nil {
			return nil, err
		}
		files[name] = entry
	}
	return files, nil
}

// runtimeOwned are the paths a container runtime provides at run time, which no
// image build decides.
var runtimeOwned = []string{"/proc", "/sys", "/dev", "/.dockerenv", "/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/run/.containerenv", driverPath}

func inventory(root string) (Inventory, error) {
	skip := map[string]bool{}
	for _, name := range runtimeOwned {
		skip[filepath.Join(root, name)] = true
	}
	tree := sha256.New()
	entries := 0
	err := filepath.WalkDir(root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip[full] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		uid, gid := owner(info)
		fmt.Fprintf(tree, "%s\x00%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.Mode(), uid, gid)
		switch {
		case info.Mode().Type() == fs.ModeSymlink:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			fmt.Fprintf(tree, "%s", target)
		case info.Mode().IsRegular():
			sum, err := fileDigest(full)
			if err != nil {
				return err
			}
			fmt.Fprintf(tree, "%d\x00%s", info.Size(), sum)
		}
		tree.Write([]byte{'\n'})
		entries++
		return nil
	})
	if err != nil {
		return Inventory{}, err
	}
	commands, err := pathCommands(root)
	if err != nil {
		return Inventory{}, err
	}
	return Inventory{
		Architecture: runtime.GOARCH,
		Tree:         "sha256:" + hex.EncodeToString(tree.Sum(nil)),
		Entries:      entries,
		Commands:     commands,
	}, nil
}

func owner(info fs.FileInfo) (uint32, uint32) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Uid, st.Gid
	}
	return 0, 0
}

func fileDigest(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pathCommands lists the executables the fixed PATH finds, following symbolic links.
func pathCommands(root string) ([]string, error) {
	found := map[string]bool{}
	for _, dir := range filepath.SplitList(searchPath) {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			info, err := os.Stat(filepath.Join(root, dir, entry.Name()))
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				found[entry.Name()] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(found)), nil
}
