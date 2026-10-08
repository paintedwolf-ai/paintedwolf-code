//go:build darwin || linux

package fseffect

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"

	"golang.org/x/sys/unix"
)

type openedRoot struct {
	path string
	file *os.File
}

type openedParent struct {
	root *openedRoot
	file *os.File
	name string
	rel  string
}

type targetAt struct{ parent *openedParent }

func openRoot(path string) (*openedRoot, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve effect root: %w", err)
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open effect root: %w", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: effect root component %q", ErrSymlink, component)
			}
			return nil, fmt.Errorf("open effect root component %q: %w", component, openErr)
		}
		fd = next
	}
	return &openedRoot{path: canonical, file: os.NewFile(uintptr(fd), canonical)}, nil
}

func (r *openedRoot) close() { _ = r.file.Close() }

func openParent(loc Location, create bool, mode os.FileMode) (*openedParent, error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return nil, err
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(clean.Rel), "/")
	name := parts[len(parts)-1]
	currentFD, err := unix.Dup(int(root.file.Fd()))
	if err != nil {
		root.close()
		return nil, fmt.Errorf("duplicate effect root: %w", err)
	}
	current := os.NewFile(uintptr(currentFD), root.path)
	parentRel := ""
	for _, component := range parts[:len(parts)-1] {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			root.close()
			return nil, fmt.Errorf("%w: component %q", ErrInvalidPath, component)
		}
		created := false
		nextFD, openErr := unix.Openat(int(current.Fd()), component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(int(current.Fd()), component, uint32(mode.Perm()))
			if mkdirErr == nil {
				created = true
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				_ = current.Close()
				root.close()
				return nil, fmt.Errorf("create effect directory %q: %w", component, mkdirErr)
			}
			nextFD, openErr = unix.Openat(int(current.Fd()), component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			_ = current.Close()
			root.close()
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: %q", ErrSymlink, component)
			}
			// PathError so os.IsNotExist recognizes a missing component.
			return nil, &fs.PathError{Op: "open effect directory", Path: component, Err: openErr}
		}
		if created {
			if syncErr := fsyncFD(int(current.Fd())); syncErr != nil {
				_ = unix.Close(nextFD)
				_ = current.Close()
				root.close()
				return nil, fmt.Errorf("sync effect directory parent: %w", syncErr)
			}
		}
		_ = current.Close()
		parentRel = filepath.Join(parentRel, component)
		current = os.NewFile(uintptr(nextFD), filepath.Join(root.path, parentRel))
	}
	return &openedParent{root: root, file: current, name: name, rel: parentRel}, nil
}

func (p *openedParent) close() {
	_ = p.file.Close()
	p.root.close()
}

func (t targetAt) Open() (*os.File, error) {
	fd, err := unix.Openat(int(t.parent.file.Fd()), t.parent.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, t.parent.name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(t.parent.root.path, t.parent.rel, t.parent.name)), nil
}

func (t targetAt) Lstat() (os.FileInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(t.parent.file.Fd()), t.parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("%w: %s", ErrSymlink, t.parent.name)
	}
	f, err := t.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}

// OpenWrite opens a target for command redirection without following symlinks.
func OpenWrite(loc Location, appendMode bool, mode os.FileMode) (*os.File, error) {
	parent, err := openParent(loc, true, 0o755)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if appendMode {
		flags |= unix.O_APPEND
	} else {
		flags |= unix.O_TRUNC
	}
	fd, err := unix.Openat(int(parent.file.Fd()), parent.name, flags, uint32(mode.Perm()))
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, parent.name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.root.path, parent.rel, parent.name)), nil
}

func platformReplace(req ReplaceRequest) (Result, error) {
	return replace(req, nil)
}

func replace(req ReplaceRequest, inject func(stage) error) (Result, error) {
	if req.Source == nil {
		return Result{}, fmt.Errorf("atomic filesystem effect request is incomplete")
	}
	parent, err := openParent(req.Location, true, req.dirMode())
	if err != nil {
		return Result{}, err
	}
	defer parent.close()
	tmpName, tmp, held, err := createTempAt(parent, 0o600)
	if err != nil {
		return Result{}, err
	}
	defer held.retire()
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !committed {
			_ = unix.Unlinkat(int(parent.file.Fd()), tmpName, 0)
		}
	}()
	if err := injectAt(inject, stageCreated); err != nil {
		return Result{}, err
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), req.Source)
	result := Result{Bytes: n, SHA256: hex.EncodeToString(hasher.Sum(nil))}
	if err != nil {
		return result, fmt.Errorf("write replacement: %w", err)
	}
	if err := injectAt(inject, stageWritten); err != nil {
		return result, err
	}
	if req.ReviewStaged != nil {
		if err := req.ReviewStaged(targetAt{parent: parent}, result); err != nil {
			return result, err
		}
	}
	unlock := lockTarget(req.Location)
	defer unlock()
	if req.ReviewStaged != nil {
		if err := verifyReviewedParent(req.Location, parent); err != nil {
			return result, err
		}
	}
	mode, err := req.resolvedMode(targetAt{parent: parent})
	if err != nil {
		return result, err
	}
	if err := tmp.Chmod(chmodMode(mode)); err != nil {
		return result, fmt.Errorf("chmod replacement: %w", err)
	}
	if err := injectAt(inject, stageModeSet); err != nil {
		return result, err
	}
	if err := fssync.File(tmp); err != nil {
		return result, fmt.Errorf("sync replacement: %w", err)
	}
	if err := injectAt(inject, stageFileSynced); err != nil {
		return result, err
	}
	if err := tmp.Close(); err != nil {
		return result, fmt.Errorf("close replacement: %w", err)
	}
	closed = true
	if err := injectAt(inject, stageFileClosed); err != nil {
		return result, err
	}
	if err := req.precondition(targetAt{parent: parent}, result); err != nil {
		return result, err
	}
	if err := injectAt(inject, stageValidated); err != nil {
		return result, err
	}
	if req.ReviewStaged != nil {
		stagedParent := *parent
		stagedParent.name = tmpName
		if err := verifyTarget(targetAt{parent: &stagedParent}, result); err != nil {
			return result, fmt.Errorf("reviewed staging changed: %w", err)
		}
	}
	if err := unix.Renameat(int(parent.file.Fd()), tmpName, int(parent.file.Fd()), parent.name); err != nil {
		return result, fmt.Errorf("commit replacement: %w", err)
	}
	committed = true
	if err := injectAt(inject, stageRenamed); err != nil {
		return result, err
	}
	if err := fsyncFD(int(parent.file.Fd())); err != nil {
		return result, fmt.Errorf("sync replacement directory: %w", err)
	}
	if err := injectAt(inject, stageDirSynced); err != nil {
		return result, err
	}
	if err := verifyTarget(targetAt{parent: parent}, result); err != nil {
		return result, err
	}
	return result, injectAt(inject, stageVerified)
}

func verifyReviewedParent(location Location, held *openedParent) error {
	current, err := openParent(location, false, 0)
	if err != nil {
		return fmt.Errorf("reviewed directory changed: %w", err)
	}
	defer current.close()
	for _, pair := range [][2]*os.File{{held.root.file, current.root.file}, {held.file, current.file}} {
		before, err := pair[0].Stat()
		if err != nil {
			return err
		}
		after, err := pair[1].Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(before, after) {
			return fmt.Errorf("reviewed directory changed: %s", location.Rel)
		}
	}
	return nil
}

// createTempAt holds the staging name before creating it so no listing sees
// the entry unregistered; the caller retires the hold after the entry is gone.
func createTempAt(parent *openedParent, mode os.FileMode) (string, *os.File, stagingHold, error) {
	directory, err := fspath.EntryIdentityAt(int(parent.file.Fd()), ".")
	if err != nil {
		return "", nil, stagingHold{}, fmt.Errorf("identify replacement parent: %w", err)
	}
	for range 64 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", nil, stagingHold{}, fmt.Errorf("randomize replacement: %w", err)
		}
		name := "." + parent.name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
		held := holdStaging(directory, name)
		fd, err := unix.Openat(int(parent.file.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
		if err != nil {
			held.abandon()
		}
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, stagingHold{}, fmt.Errorf("create replacement: %w", err)
		}
		return name, os.NewFile(uintptr(fd), filepath.Join(parent.root.path, parent.rel, name)), held, nil
	}
	return "", nil, stagingHold{}, fmt.Errorf("create replacement: exhausted unique names")
}

func verifyTarget(target Target, want Result) error {
	f, err := target.Open()
	if err != nil {
		return fmt.Errorf("%w: open destination: %w", ErrPostcondition, err)
	}
	defer func() { _ = f.Close() }()
	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil {
		return fmt.Errorf("%w: read destination: %w", ErrPostcondition, err)
	}
	if n != want.Bytes || hex.EncodeToString(hasher.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
	}
	return nil
}

// Remove unlinks a file or empty directory without following symlinks.
func Remove(req RemoveRequest) error {
	parent, err := openParent(req.Location, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	info, err := (targetAt{parent: parent}).Lstat()
	if err != nil {
		return err
	}
	flags := 0
	if info.IsDir() {
		flags = unix.AT_REMOVEDIR
	}
	if req.BeforeCommit != nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("conditional remove requires a regular file")
		}
		return removeRegularConditionally(parent, req)
	}
	if err := unix.Unlinkat(int(parent.file.Fd()), parent.name, flags); err != nil {
		return err
	}
	return fsyncFD(int(parent.file.Fd()))
}

func removeRegularConditionally(parent *openedParent, req RemoveRequest) error {
	quarantine, err := unusedRemoveName(parent)
	if err != nil {
		return err
	}
	parentFD := int(parent.file.Fd())
	directory, err := fspath.EntryIdentityAt(parentFD, ".")
	if err != nil {
		return fmt.Errorf("identify removal parent: %w", err)
	}
	held := holdStaging(directory, quarantine)
	if err := unix.Renameat(parentFD, parent.name, parentFD, quarantine); err != nil {
		held.abandon()
		return fmt.Errorf("stage removal: %w", err)
	}
	defer held.retire()
	stagedParent := *parent
	stagedParent.name = quarantine
	if err := req.BeforeCommit(targetAt{parent: &stagedParent}); err != nil {
		// A hard-link restore cannot replace a concurrent writer's entry.
		if restoreErr := unix.Linkat(parentFD, quarantine, parentFD, parent.name, 0); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("preserve refused removal at %q: %w", quarantine, restoreErr))
		}
		if unlinkErr := unix.Unlinkat(parentFD, quarantine, 0); unlinkErr != nil {
			return errors.Join(err, fmt.Errorf("finish refused removal restore: %w", unlinkErr))
		}
		if syncErr := fsyncFD(parentFD); syncErr != nil {
			return errors.Join(err, syncErr)
		}
		return err
	}
	if err := unix.Unlinkat(parentFD, quarantine, 0); err != nil {
		return fmt.Errorf("commit removal: %w", err)
	}
	return fsyncFD(parentFD)
}

func unusedRemoveName(parent *openedParent) (string, error) {
	for range 64 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", fmt.Errorf("randomize removal: %w", err)
		}
		name := "." + parent.name + "." + hex.EncodeToString(nonce[:]) + ".remove"
		var st unix.Stat_t
		err := unix.Fstatat(int(parent.file.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect removal staging name: %w", err)
		}
	}
	return "", fmt.Errorf("stage removal: exhausted unique names")
}

// Rename atomically moves a target beneath one verified root.
func Rename(root, fromRel, toRel string) error {
	return renameEntry(root, fromRel, toRel, true, "")
}

// RenameNoReplace publishes an entry without overwriting a concurrent occupant.
func RenameNoReplace(root, fromRel, toRel string) error {
	return renameEntry(root, fromRel, toRel, false, "")
}

// RenameGuarded refuses replacement and checks the source through its held parent.
func RenameGuarded(root, fromRel, toRel, expected string) error {
	if expected == "" {
		return ErrPostcondition
	}
	return renameEntry(root, fromRel, toRel, false, expected)
}

func renameEntry(root, fromRel, toRel string, replace bool, expected string) error {
	from, err := openParent(Location{Root: root, Rel: fromRel}, false, 0)
	if err != nil {
		return err
	}
	defer from.close()
	to, err := openParent(Location{Root: root, Rel: toRel}, true, 0o755)
	if err != nil {
		return err
	}
	defer to.close()
	var sourceStat unix.Stat_t
	if err := unix.Fstatat(int(from.file.Fd()), from.name, &sourceStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if expected != "" {
		identity, err := fspath.EntryIdentityAt(int(from.file.Fd()), from.name)
		if err != nil {
			return err
		}
		if identity != expected {
			return ErrPostcondition
		}
	}
	rename := unix.Renameat
	if !replace {
		rename = renameNoReplace
	}
	if err := rename(int(from.file.Fd()), from.name, int(to.file.Fd()), to.name); err != nil {
		return err
	}
	if err := fsyncFD(int(from.file.Fd())); err != nil {
		return err
	}
	if int(from.file.Fd()) != int(to.file.Fd()) {
		return fsyncFD(int(to.file.Fd()))
	}
	return nil
}

// refuseSymlinkTarget rejects a target entry that is itself a symlink.
func refuseSymlinkTarget(parent *openedParent) error {
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.file.Fd()), parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "stat effect target", Path: parent.name, Err: err}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("%w: %s", ErrSymlink, parent.name)
	}
	return nil
}

// UpdateMode applies a guarded permission transition to a held regular file.
func UpdateMode(req ModeUpdateRequest) (ModeUpdateResult, error) {
	unlock := lockTarget(req.Location)
	defer unlock()
	parent, err := openParent(req.Location, false, 0)
	if err != nil {
		return ModeUpdateResult{}, err
	}
	defer parent.close()
	if err := refuseSymlinkTarget(parent); err != nil {
		return ModeUpdateResult{}, err
	}
	f, err := (targetAt{parent: parent}).Open()
	if err != nil {
		return ModeUpdateResult{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ModeUpdateResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ModeUpdateResult{}, &fs.PathError{Op: "chmod effect target", Path: parent.name, Err: unix.EINVAL}
	}
	if req.BeforeCommit != nil {
		if err := req.BeforeCommit(f, info); err != nil {
			return ModeUpdateResult{}, err
		}
	}
	result, appliedMode := resolveModeUpdate(info.Mode(), req.Update)
	if result.After != result.Before {
		if err := f.Chmod(appliedMode); err != nil {
			return ModeUpdateResult{}, &fs.PathError{Op: "chmod effect target", Path: parent.name, Err: err}
		}
	}
	return result, nil
}

// Chown applies ownership to a target reached without symlink traversal.
func Chown(loc Location, uid, gid int) error {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	if err := refuseSymlinkTarget(parent); err != nil {
		return err
	}
	if err := unix.Fchownat(int(parent.file.Fd()), parent.name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "chown effect target", Path: parent.name, Err: err}
	}
	return nil
}

// MkdirAll creates every missing component without traversing symlinks.
func MkdirAll(loc Location, mode os.FileMode) error {
	clean, err := cleanLocation(loc)
	if err != nil {
		return err
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return err
	}
	defer root.close()
	fd, err := unix.Dup(int(root.file.Fd()))
	if err != nil {
		return err
	}
	current := os.NewFile(uintptr(fd), root.path)
	defer func() { _ = current.Close() }()
	anyCreated := false
	for _, component := range strings.Split(filepath.ToSlash(clean.Rel), "/") {
		created := false
		next, openErr := unix.Openat(int(current.Fd()), component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(int(current.Fd()), component, uint32(mode.Perm()))
			if mkdirErr == nil {
				created = true
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				return mkdirErr
			}
			next, openErr = unix.Openat(int(current.Fd()), component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return fmt.Errorf("%w: %q", ErrSymlink, component)
			}
			return openErr
		}
		if created {
			anyCreated = true
			if err := fsyncFD(int(current.Fd())); err != nil {
				_ = unix.Close(next)
				return err
			}
		}
		_ = current.Close()
		current = os.NewFile(uintptr(next), component)
	}
	if !anyCreated {
		return nil
	}
	return fsyncFD(int(current.Fd()))
}
