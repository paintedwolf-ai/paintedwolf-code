//go:build linux

package desktoptrash

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
	"golang.org/x/sys/unix"
)

const procSelfMountinfo = "/proc/self/mountinfo"

var relocateGuarded = fseffect.RelocateGuarded

func resolveMountDir(dir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// sameMount compares VFS mount IDs (STATX_MNT_ID) so bind mounts on the same
// underlying device are treated as distinct mounts while unmounted btrfs
// subvolumes inside a mount stay with their enclosing mount.
func sameMount(dirA, dirB string) (bool, error) {
	idA, okA, err := statxMountID(dirA)
	if err != nil {
		return false, err
	}
	idB, okB, err := statxMountID(dirB)
	if err != nil {
		return false, err
	}
	if okA && okB {
		return idA == idB, nil
	}
	return fspath.SameFilesystem(dirA, dirB)
}

func statxMountID(dir string) (uint64, bool, error) {
	var sx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, dir, 0, unix.STATX_MNT_ID, &sx)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 {
		return 0, false, nil
	}
	return sx.Mnt_id, true, nil
}

func mountPointFromProc(mntID uint64, dir string) (string, bool) {
	file, err := os.Open(procSelfMountinfo)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	top, ok := findMountPoint(file, mntID)
	if !ok || !isWithinMount(top, dir) {
		return "", false
	}
	topID, hasID, err := statxMountID(top)
	if err != nil || !hasID || topID != mntID {
		return "", false
	}
	return top, true
}

func findMountPoint(r io.Reader, targetMountID uint64) (string, bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		mntID, mountPoint, ok := parseMountinfoLine(scanner.Text())
		if ok && mntID == targetMountID {
			return mountPoint, true
		}
	}
	return "", false
}

func parseMountinfoLine(line string) (uint64, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return 0, "", false
	}
	sep := -1
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			sep = i
			break
		}
	}
	if sep < 6 || len(fields) < sep+4 {
		return 0, "", false
	}
	mntID, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	mountPoint := unescapeMountPath(fields[4])
	if !filepath.IsAbs(mountPoint) {
		return 0, "", false
	}
	return mntID, filepath.Clean(mountPoint), true
}

// unescapeMountPath decodes the 3-digit octal escapes (\040, \011, \012, \134)
// emitted by Linux show_mountinfo for mount paths.
func unescapeMountPath(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) && raw[i+1] <= '3' && isOctalDigit(raw[i+1]) && isOctalDigit(raw[i+2]) && isOctalDigit(raw[i+3]) {
			b := (raw[i+1]-'0')<<6 | (raw[i+2]-'0')<<3 | (raw[i+3] - '0')
			out.WriteByte(b)
			i += 3
			continue
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

func isOctalDigit(b byte) bool { return b >= '0' && b <= '7' }

func isWithinMount(top, dir string) bool {
	rel, err := filepath.Rel(top, dir)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// relocateLinuxEntry moves an entry within its mount. Linux btrfs returns EXDEV
// from renameat2 across subvolumes even when both directories share the same
// STATX_MNT_ID mount; in that case we relocate into the destination on the
// same mount and remove the guarded source without crossing mounts.
func relocateLinuxEntry(from, to fseffect.Location, identity string) error {
	err := relocateGuarded(from, to, identity)
	if !errors.Is(err, unix.EXDEV) {
		return err
	}
	fromParent := filepath.Dir(filepath.Join(from.Root, from.Rel))
	toParent := filepath.Dir(filepath.Join(to.Root, to.Rel))
	if same, mountErr := sameMount(fromParent, toParent); mountErr != nil || !same {
		return err
	}
	return relocateSameMountSubvolume(from, to, identity)
}

func relocateSameMountSubvolume(from, to fseffect.Location, identity string) error {
	fromPath := filepath.Join(from.Root, from.Rel)
	current, err := fspath.EntryIdentity(fromPath)
	if err != nil {
		return err
	}
	if current != identity {
		return fseffect.ErrPostcondition
	}
	srcRoot, err := os.OpenRoot(from.Root)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := os.OpenRoot(to.Root)
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()
	if _, statErr := dstRoot.Lstat(to.Rel); statErr == nil {
		return os.ErrExist
	}
	if err := cloneSubvolumeEntry(srcRoot, from.Rel, dstRoot, to.Rel); err != nil {
		if !errors.Is(err, os.ErrExist) {
			_ = dstRoot.RemoveAll(to.Rel)
		}
		return err
	}
	if err := fseffect.SyncDirectory(dstRoot, filepath.Dir(to.Rel)); err != nil {
		_ = dstRoot.RemoveAll(to.Rel)
		return err
	}
	after, err := fspath.EntryIdentity(fromPath)
	if err != nil || after != identity {
		_ = dstRoot.RemoveAll(to.Rel)
		if err != nil {
			return err
		}
		return fseffect.ErrPostcondition
	}
	return fseffect.RemoveTreeGuarded(from, identity)
}

func cloneSubvolumeEntry(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string) error {
	info, err := srcRoot.Lstat(srcRel)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := srcRoot.Readlink(srcRel)
		if err != nil {
			return err
		}
		return dstRoot.Symlink(link, dstRel)
	case info.IsDir():
		return cloneSubvolumeDir(srcRoot, srcRel, dstRoot, dstRel, info)
	case info.Mode().IsRegular():
		return cloneSubvolumeFile(srcRoot, srcRel, dstRoot, dstRel, info)
	default:
		return unix.EXDEV
	}
}

func cloneSubvolumeDir(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, info os.FileInfo) error {
	if err := dstRoot.Mkdir(dstRel, 0o700); err != nil {
		return err
	}
	srcDir, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	names, readErr := srcDir.Readdirnames(-1)
	if err := errors.Join(readErr, srcDir.Close()); err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := cloneSubvolumeEntry(srcRoot, filepath.Join(srcRel, name), dstRoot, filepath.Join(dstRel, name)); err != nil {
			return err
		}
	}
	return syncClonedEntry(dstRoot, dstRel, info)
}

func cloneSubvolumeFile(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, info os.FileInfo) error {
	in, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	cloned, err := fileclone.CloneInto(in, dstRoot, dstRel)
	if err != nil {
		return err
	}
	if !cloned {
		out, err := dstRoot.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		if err := errors.Join(copyErr, out.Close()); err != nil {
			return err
		}
	}
	return syncClonedEntry(dstRoot, dstRel, info)
}

func syncClonedEntry(dstRoot *os.Root, dstRel string, info os.FileInfo) error {
	f, err := dstRoot.Open(dstRel)
	if err != nil {
		return err
	}
	err = dstRoot.Chtimes(dstRel, time.Now(), info.ModTime())
	if err == nil {
		err = f.Chmod(info.Mode())
	}
	if err == nil {
		err = fssync.File(f)
	}
	return errors.Join(err, f.Close())
}
