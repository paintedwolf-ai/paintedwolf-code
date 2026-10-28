//go:build darwin || linux

package fseffect

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"

	"golang.org/x/sys/unix"
)

type rootCacheEntry struct {
	rawPath   string
	canonical string
	dev       uint64
	ino       uint64
}

type sharedParentDir struct {
	rawRoot  string
	rel      string
	rootPath string
	rootDev  uint64
	rootIno  uint64
	dev      uint64
	ino      uint64
	fd       int
	refs     atomic.Int32
}

func (s *sharedParentDir) release() {
	if s.refs.Add(-1) == 0 && s.fd >= 0 {
		_ = unix.Close(s.fd)
		s.fd = -1
	}
}

var (
	tempNonceSeed    [8]byte
	tempNonceCounter atomic.Uint64
	rootCache        struct {
		mu      sync.RWMutex
		entries [8]rootCacheEntry
		next    int
	}
	parentIDCache struct {
		mu       sync.RWMutex
		dev      uint64
		ino      uint64
		identity string
	}
	sharedParentCache struct {
		mu  sync.RWMutex
		dir *sharedParentDir
	}
)

func init() {
	_, _ = rand.Read(tempNonceSeed[:])
}

type openedRoot struct {
	path string
	fd   int
	dev  uint64
	ino  uint64
}

type openedParent struct {
	rootPath string
	rootDev  uint64
	rootIno  uint64
	dev      uint64
	ino      uint64
	fd       int
	name     string
	rel      string
	shared   *sharedParentDir
}

type targetAt struct{ parent *openedParent }

type fdWriter int

func (w fdWriter) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := unix.Write(int(w), p[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func openRoot(path string) (*openedRoot, error) {
	return openRootFlags(path, unix.O_RDONLY)
}

func openRootFlags(path string, accessFlags int) (*openedRoot, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		if fd, err := unix.Open(clean, accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0); err == nil {
			var st unix.Stat_t
			if unix.Fstat(fd, &st) == nil {
				rootCache.mu.RLock()
				var canonical string
				for i := range rootCache.entries {
					e := &rootCache.entries[i]
					if (e.rawPath == clean || e.canonical == clean) && e.dev == deviceID(&st) && e.ino == st.Ino {
						canonical = e.canonical
						break
					}
				}
				rootCache.mu.RUnlock()
				if canonical != "" {
					return &openedRoot{path: canonical, fd: fd, dev: deviceID(&st), ino: st.Ino}, nil
				}
			}
			_ = unix.Close(fd)
		}
		if root, err := walkRootNoFollow(clean, clean, accessFlags); err == nil {
			return root, nil
		}
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve effect root: %w", err)
	}
	return walkRootNoFollow(clean, canonical, accessFlags)
}

func walkRootNoFollow(rawPath, canonical string, accessFlags int) (*openedRoot, error) {
	fd, err := unix.Open(string(filepath.Separator), accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open effect root: %w", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component,
			accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: effect root component %q", ErrSymlink, component)
			}
			return nil, fmt.Errorf("open effect root component %q: %w", component, openErr)
		}
		fd = next
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) == nil {
		rootCache.mu.Lock()
		stored := false
		for i := range rootCache.entries {
			if (rootCache.entries[i].rawPath == rawPath && rootCache.entries[i].canonical == canonical) || rootCache.entries[i].canonical == "" {
				rootCache.entries[i] = rootCacheEntry{rawPath: rawPath, canonical: canonical, dev: deviceID(&st), ino: st.Ino}
				stored = true
				break
			}
		}
		if !stored {
			rootCache.entries[rootCache.next] = rootCacheEntry{rawPath: rawPath, canonical: canonical, dev: deviceID(&st), ino: st.Ino}
			rootCache.next = (rootCache.next + 1) % len(rootCache.entries)
		}
		rootCache.mu.Unlock()
	}
	return &openedRoot{path: canonical, fd: fd, dev: deviceID(&st), ino: st.Ino}, nil
}

func (r *openedRoot) close() {
	if r.fd >= 0 {
		_ = unix.Close(r.fd)
		r.fd = -1
	}
}

func openParent(loc Location, create bool, mode os.FileMode) (*openedParent, error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(clean.Rel), "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 {
		parentRel := filepath.FromSlash(strings.Join(parts[:len(parts)-1], "/"))
		sharedParentCache.mu.RLock()
		d := sharedParentCache.dir
		if d != nil && d.rawRoot == clean.Root && d.rel == parentRel && d.fd >= 0 {
			d.refs.Add(1)
			sharedParentCache.mu.RUnlock()
			var st unix.Stat_t
			if unix.Fstatat(unix.AT_FDCWD, filepath.Join(d.rootPath, d.rel), &st, unix.AT_SYMLINK_NOFOLLOW) == nil && deviceID(&st) == d.dev && st.Ino == d.ino {
				return &openedParent{
					rootPath: d.rootPath, rootDev: d.rootDev, rootIno: d.rootIno,
					dev: d.dev, ino: d.ino, fd: d.fd, name: name, rel: d.rel, shared: d,
				}, nil
			}
			d.release()
		} else {
			sharedParentCache.mu.RUnlock()
		}
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return nil, err
	}
	if len(parts) == 1 {
		return &openedParent{rootPath: root.path, rootDev: root.dev, rootIno: root.ino, dev: root.dev, ino: root.ino, fd: root.fd, name: name}, nil
	}
	currentFD := root.fd
	ownsCurrent := false
	closeCurrent := func() {
		if ownsCurrent {
			_ = unix.Close(currentFD)
		}
	}
	parentRel := ""
	for _, component := range parts[:len(parts)-1] {
		if component == "" || component == "." || component == ".." {
			closeCurrent()
			root.close()
			return nil, fmt.Errorf("%w: component %q", ErrInvalidPath, component)
		}
		created := false
		nextFD, openErr := unix.Openat(currentFD, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(currentFD, component, uint32(mode.Perm()))
			if mkdirErr == nil {
				created = true
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				closeCurrent()
				root.close()
				return nil, fmt.Errorf("create effect directory %q: %w", component, mkdirErr)
			}
			nextFD, openErr = unix.Openat(currentFD, component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			closeCurrent()
			root.close()
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: %q", ErrSymlink, component)
			}
			// PathError so os.IsNotExist recognizes a missing component.
			return nil, &fs.PathError{Op: "open effect directory", Path: component, Err: openErr}
		}
		if created {
			if syncErr := fsyncFD(currentFD); syncErr != nil {
				_ = unix.Close(nextFD)
				closeCurrent()
				root.close()
				return nil, fmt.Errorf("sync effect directory parent: %w", syncErr)
			}
		}
		closeCurrent()
		parentRel = filepath.Join(parentRel, component)
		currentFD = nextFD
		ownsCurrent = true
	}
	rootPath, rootDev, rootIno := root.path, root.dev, root.ino
	root.close()
	var st unix.Stat_t
	if unix.Fstat(currentFD, &st) == nil && st.Ino != 0 {
		d := &sharedParentDir{
			rawRoot: clean.Root, rel: parentRel,
			rootPath: rootPath, rootDev: rootDev, rootIno: rootIno,
			dev: deviceID(&st), ino: st.Ino, fd: currentFD,
		}
		d.refs.Store(2)
		sharedParentCache.mu.Lock()
		old := sharedParentCache.dir
		sharedParentCache.dir = d
		sharedParentCache.mu.Unlock()
		if old != nil {
			old.release()
		}
		return &openedParent{
			rootPath: rootPath, rootDev: rootDev, rootIno: rootIno,
			dev: deviceID(&st), ino: st.Ino, fd: currentFD, name: name, rel: parentRel, shared: d,
		}, nil
	}
	return &openedParent{rootPath: rootPath, rootDev: rootDev, rootIno: rootIno, fd: currentFD, name: name, rel: parentRel}, nil
}

func (p *openedParent) close() {
	if p.shared != nil {
		p.shared.release()
		p.shared = nil
		p.fd = -1
		return
	}
	if p.fd >= 0 {
		_ = unix.Close(p.fd)
		p.fd = -1
	}
}

func (t targetAt) Open() (*os.File, error) {
	fd, err := unix.Openat(t.parent.fd, t.parent.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, t.parent.name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(t.parent.rootPath, t.parent.rel, t.parent.name)), nil
}

func (t targetAt) Lstat() (os.FileInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(t.parent.fd, t.parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
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
	fd, err := unix.Openat(parent.fd, parent.name, flags, uint32(mode.Perm()))
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, parent.name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.rootPath, parent.rel, parent.name)), nil
}

func stageReplace(req ReplaceRequest, inject func(stage) error) (*Replacement, Result, error) {
	if req.Source == nil {
		return nil, Result{}, fmt.Errorf("atomic filesystem effect request is incomplete")
	}
	parent, err := openParent(req.Location, true, req.dirMode())
	if err != nil {
		return nil, Result{}, err
	}
	initialMode := os.FileMode(0o600)
	if !req.PreserveMode && req.Mode != 0 {
		initialMode = chmodMode(req.Mode)
	}
	tmpName, tmpFD, held, err := createTempAt(parent, initialMode)
	if err != nil {
		parent.close()
		return nil, Result{}, err
	}
	closed := false
	committed := false
	cleanup := func() {
		if !closed {
			_ = unix.Close(tmpFD)
		}
		if !committed {
			_ = unix.Unlinkat(parent.fd, tmpName, 0)
		}
		held.retire()
		parent.close()
	}
	ready := false
	defer func() {
		if !ready {
			cleanup()
		}
	}()
	if err := injectAt(inject, stageCreated); err != nil {
		return nil, Result{}, err
	}
	hasher := sha256.New()
	copyBuf := req.CopyBuffer
	if len(copyBuf) == 0 {
		var stackBuf [32 << 10]byte
		copyBuf = stackBuf[:]
	}
	n, err := io.CopyBuffer(io.MultiWriter(fdWriter(tmpFD), hasher), req.Source, copyBuf)
	result := Result{Bytes: n, SHA256: hex.EncodeToString(hasher.Sum(nil))}
	if err != nil {
		return nil, result, fmt.Errorf("write replacement: %w", err)
	}
	var stagedStat unix.Stat_t
	stagedStatOK := inject == nil && unix.Fstat(tmpFD, &stagedStat) == nil
	if err := injectAt(inject, stageWritten); err != nil {
		return nil, result, err
	}
	if req.ReviewStaged != nil {
		if err := req.ReviewStaged(targetAt{parent: parent}, result); err != nil {
			return nil, result, err
		}
	}
	ready = true
	staged := &Replacement{result: result, close: cleanup}
	staged.publish = func() (Result, error) {
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
		targetMode := posixChmodMode(chmodMode(mode))
		if !stagedStatOK || uint32(stagedStat.Mode)&0o7777 != targetMode {
			if err := unix.Fchmod(tmpFD, targetMode); err != nil {
				return result, fmt.Errorf("chmod replacement: %w", err)
			}
			stagedStatOK = false
		}
		if err := injectAt(inject, stageModeSet); err != nil {
			return result, err
		}
		if err := fsyncFD(tmpFD); err != nil {
			return result, fmt.Errorf("sync replacement: %w", err)
		}
		if err := injectAt(inject, stageFileSynced); err != nil {
			return result, err
		}
		if err := unix.Close(tmpFD); err != nil {
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
		stagingUnchanged := false
		if stagedStatOK {
			var currentStat unix.Stat_t
			if err := unix.Fstatat(parent.fd, tmpName, &currentStat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
				stagingUnchanged = currentStat.Dev == stagedStat.Dev &&
					currentStat.Ino == stagedStat.Ino &&
					currentStat.Mode == stagedStat.Mode &&
					currentStat.Nlink == 1 &&
					currentStat.Size == result.Bytes &&
					currentStat.Mtim == stagedStat.Mtim &&
					currentStat.Ctim == stagedStat.Ctim
			}
		}
		if req.ReviewStaged != nil && !stagingUnchanged {
			stagedParent := *parent
			stagedParent.name = tmpName
			if err := verifyTarget(targetAt{parent: &stagedParent}, result, req.CopyBuffer); err != nil {
				return result, fmt.Errorf("reviewed staging changed: %w", err)
			}
		}
		if err := unix.Renameat(parent.fd, tmpName, parent.fd, parent.name); err != nil {
			return result, fmt.Errorf("commit replacement: %w", err)
		}
		committed = true
		result.Publication = PublicationReplaced
		if err := injectAt(inject, stageRenamed); err != nil {
			return result, err
		}
		if err := fsyncFD(parent.fd); err != nil {
			return result, fmt.Errorf("sync replacement directory: %w", err)
		}
		result.Publication = PublicationDurable
		if err := injectAt(inject, stageDirSynced); err != nil {
			return result, err
		}
		verifiedFast := false
		if stagingUnchanged {
			var postStat unix.Stat_t
			if err := unix.Fstatat(parent.fd, parent.name, &postStat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
				verifiedFast = postStat.Dev == stagedStat.Dev &&
					postStat.Ino == stagedStat.Ino &&
					postStat.Mode == stagedStat.Mode &&
					postStat.Size == result.Bytes &&
					postStat.Mtim == stagedStat.Mtim
			}
		}
		if !verifiedFast {
			if err := verifyTarget(targetAt{parent: parent}, result, req.CopyBuffer); err != nil {
				return result, err
			}
		}
		result.Publication = PublicationVerified
		return result, injectAt(inject, stageVerified)
	}
	return staged, result, nil
}

func posixChmodMode(mode os.FileMode) uint32 {
	out := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		out |= unix.S_ISUID
	}
	if mode&os.ModeSetgid != 0 {
		out |= unix.S_ISGID
	}
	if mode&os.ModeSticky != 0 {
		out |= unix.S_ISVTX
	}
	return out
}

func verifyReviewedParent(location Location, held *openedParent) error {
	if held.dev != 0 && held.ino != 0 {
		targetPath := held.rootPath
		if held.rel != "" {
			targetPath = filepath.Join(held.rootPath, held.rel)
		}
		var after unix.Stat_t
		if err := unix.Fstatat(unix.AT_FDCWD, targetPath, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("reviewed directory changed: %w", err)
		}
		if held.dev != deviceID(&after) || held.ino != after.Ino {
			return fmt.Errorf("reviewed directory changed: %s", location.Rel)
		}
		return nil
	}
	current, err := openParent(location, false, 0)
	if err != nil {
		return fmt.Errorf("reviewed directory changed: %w", err)
	}
	defer current.close()
	if held.rootDev != current.rootDev || held.rootIno != current.rootIno {
		return fmt.Errorf("reviewed directory changed: %s", location.Rel)
	}
	var before, after unix.Stat_t
	if err := unix.Fstat(held.fd, &before); err != nil {
		return err
	}
	if err := unix.Fstat(current.fd, &after); err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino {
		return fmt.Errorf("reviewed directory changed: %s", location.Rel)
	}
	return nil
}

func parentDirectoryIdentity(parent *openedParent) (string, error) {
	fd := parent.fd
	if parent.ino == 0 {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil {
			parent.dev, parent.ino = deviceID(&st), st.Ino
		}
	}
	if parent.ino != 0 {
		parentIDCache.mu.RLock()
		if parentIDCache.dev == parent.dev && parentIDCache.ino == parent.ino && parentIDCache.identity != "" {
			id := parentIDCache.identity
			parentIDCache.mu.RUnlock()
			return id, nil
		}
		parentIDCache.mu.RUnlock()
	}
	id, err := fspath.EntryIdentityAt(fd, ".")
	if err != nil {
		return "", err
	}
	if parent.ino != 0 {
		parentIDCache.mu.Lock()
		parentIDCache.dev, parentIDCache.ino, parentIDCache.identity = parent.dev, parent.ino, id
		parentIDCache.mu.Unlock()
	}
	return id, nil
}

// createTempAt holds the staging name before creating it so no listing sees
// the entry unregistered; the caller retires the hold after the entry is gone.
func createTempAt(parent *openedParent, mode os.FileMode) (string, int, stagingHold, error) {
	directory, err := parentDirectoryIdentity(parent)
	if err != nil {
		return "", -1, stagingHold{}, fmt.Errorf("identify replacement parent: %w", err)
	}
	for attempt := range 64 {
		var nonce [12]byte
		if attempt == 0 {
			copy(nonce[:8], tempNonceSeed[:])
			binary.LittleEndian.PutUint32(nonce[8:], uint32(tempNonceCounter.Add(1)))
		} else if _, err := rand.Read(nonce[:]); err != nil {
			return "", -1, stagingHold{}, fmt.Errorf("randomize replacement: %w", err)
		}
		name := "." + parent.name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
		held := holdStaging(directory, name)
		fd, err := unix.Openat(parent.fd, name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
		if err != nil {
			held.abandon()
		}
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, stagingHold{}, fmt.Errorf("create replacement: %w", err)
		}
		return name, fd, held, nil
	}
	return "", -1, stagingHold{}, fmt.Errorf("create replacement: exhausted unique names")
}

func verifyTarget(target Target, want Result, buffer []byte) error {
	if ta, ok := target.(targetAt); ok {
		fd, err := unix.Openat(ta.parent.fd, ta.parent.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, unix.ELOOP) {
				err = fmt.Errorf("%w: %s", ErrSymlink, ta.parent.name)
			}
			return fmt.Errorf("%w: open destination: %w", ErrPostcondition, err)
		}
		defer func() { _ = unix.Close(fd) }()
		buf := buffer
		if len(buf) == 0 {
			var stackBuf [32 << 10]byte
			buf = stackBuf[:]
		}
		if want.Bytes >= 0 && want.Bytes < int64(len(buf)) {
			n, readErr := unix.Read(fd, buf[:want.Bytes+1])
			if readErr == nil && int64(n) == want.Bytes {
				sum := sha256.Sum256(buf[:n])
				if hex.EncodeToString(sum[:]) != want.SHA256 {
					return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
				}
				return nil
			}
			if readErr != nil && !errors.Is(readErr, unix.EINTR) {
				return fmt.Errorf("%w: read destination: %w", ErrPostcondition, readErr)
			}
			if int64(n) > want.Bytes {
				return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
			}
			_, _ = unix.Seek(fd, 0, io.SeekStart)
		}
		hasher := sha256.New()
		var total int64
		for {
			n, readErr := unix.Read(fd, buf)
			if errors.Is(readErr, unix.EINTR) {
				continue
			}
			if n > 0 {
				total += int64(n)
				_, _ = hasher.Write(buf[:n])
			}
			if readErr != nil {
				return fmt.Errorf("%w: read destination: %w", ErrPostcondition, readErr)
			}
			if n == 0 {
				break
			}
		}
		if total != want.Bytes || hex.EncodeToString(hasher.Sum(nil)) != want.SHA256 {
			return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
		}
		return nil
	}
	f, err := target.Open()
	if err != nil {
		return fmt.Errorf("%w: open destination: %w", ErrPostcondition, err)
	}
	defer func() { _ = f.Close() }()
	hasher := sha256.New()
	n, err := io.CopyBuffer(hasher, struct{ io.Reader }{f}, buffer)
	if err != nil {
		return fmt.Errorf("%w: read destination: %w", ErrPostcondition, err)
	}
	if n != want.Bytes || hex.EncodeToString(hasher.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
	}
	return nil
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

// RelocateGuarded moves an identified entry between held roots without replacing a destination.
func RelocateGuarded(from, to Location, expected string) error {
	if expected == "" {
		return ErrPostcondition
	}
	return relocateEntry(from, to, false, expected)
}

func renameEntry(root, fromRel, toRel string, replace bool, expected string) error {
	return relocateEntry(Location{Root: root, Rel: fromRel}, Location{Root: root, Rel: toRel}, replace, expected)
}

func relocateEntry(source, destination Location, replace bool, expected string) error {
	from, err := openParent(source, false, 0)
	if err != nil {
		return err
	}
	defer from.close()
	to, err := openParent(destination, true, 0o755)
	if err != nil {
		return err
	}
	defer to.close()
	var sourceStat unix.Stat_t
	if err := unix.Fstatat(from.fd, from.name, &sourceStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if expected != "" {
		identity, err := fspath.EntryIdentityAt(from.fd, from.name)
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
	if err := rename(from.fd, from.name, to.fd, to.name); err != nil {
		return err
	}
	if err := fsyncFD(from.fd); err != nil {
		return err
	}
	if from.fd != to.fd {
		return fsyncFD(to.fd)
	}
	return nil
}

// refuseSymlinkTarget rejects a target entry that is itself a symlink.
func refuseSymlinkTarget(parent *openedParent) error {
	var st unix.Stat_t
	if err := unix.Fstatat(parent.fd, parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
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
	if !info.Mode().IsRegular() && !info.IsDir() {
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

// platformChown applies ownership to a target reached without symlink traversal.
func platformChown(loc Location, uid, gid int) error {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	if err := refuseSymlinkTarget(parent); err != nil {
		return err
	}
	if err := unix.Fchownat(parent.fd, parent.name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "chown effect target", Path: parent.name, Err: err}
	}
	return nil
}

// platformTimes sets times on a target reached without symlink traversal.
func platformTimes(loc Location, access, modify *time.Time) error {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	if err := refuseSymlinkTarget(parent); err != nil {
		return err
	}
	spec := func(t *time.Time) unix.Timespec {
		if t == nil {
			return unix.Timespec{Nsec: utimeOmit}
		}
		return unix.NsecToTimespec(t.UnixNano())
	}
	times := []unix.Timespec{spec(access), spec(modify)}
	if err := unix.UtimesNanoAt(parent.fd, parent.name, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "timestamp effect target", Path: parent.name, Err: err}
	}
	return nil
}

// deviceID widens Stat_t.Dev, which is int32 on Darwin and uint64 on Linux.
func deviceID(st *unix.Stat_t) uint64 {
	return uint64(st.Dev)
}
