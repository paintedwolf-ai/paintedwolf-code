//go:build linux

package desktoptrash

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
)

func platformMove(path string) (Receipt, error) {
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return Receipt{}, err
	}
	trash, origin, err := linuxTrashFor(path)
	if err != nil {
		return Receipt{}, err
	}
	files, metadata := filepath.Join(trash, "files"), filepath.Join(trash, "info")
	for _, directory := range []string{trash, files, metadata} {
		if err := privateTrashDirectory(directory); err != nil {
			return Receipt{}, err
		}
	}
	name := uuid.NewString()
	infoPath := filepath.Join(metadata, name+".trashinfo")
	info, err := os.OpenFile(infoPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G703 -- UUID name under the checked private trash directory.
	if err != nil {
		return Receipt{}, err
	}
	_, writeErr := fmt.Fprintf(info, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", encodeTrashPath(origin), time.Now().Format("2006-01-02T15:04:05"))
	if writeErr == nil {
		writeErr = fssync.File(info)
	}
	if err := errors.Join(writeErr, info.Close()); err != nil {
		_ = os.Remove(infoPath) // #nosec G703 -- Removes only the UUID metadata file created above.
		return Receipt{}, err
	}
	directory, err := os.Open(metadata) // #nosec G703 -- metadata was checked as a private directory above.
	if err != nil {
		return Receipt{}, err
	}
	syncErr := errors.Join(fssync.File(directory), directory.Close())
	if syncErr != nil {
		return Receipt{}, syncErr
	}
	receipt := Receipt{Path: filepath.Join(files, name), Metadata: infoPath}
	err = relocateLinuxEntry(fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, fseffect.Location{Root: files, Rel: name}, identity)
	if err != nil {
		if _, statErr := os.Lstat(path); statErr == nil { // #nosec G703 -- This checks the caller's selected source after a guarded move failed.
			_ = os.Remove(infoPath) // #nosec G703 -- Removes only the UUID metadata file created above.
		}
		return receipt, err
	}
	return receipt, nil
}

func restoreEntry(receipt Receipt, destination fseffect.Location) error {
	return relocateLinuxEntry(fseffect.Location{Root: filepath.Dir(receipt.Path), Rel: filepath.Base(receipt.Path)}, destination, receipt.Identity)
}

// File managers decode the recorded path as a URL, so every byte outside the
// unreserved set is percent-encoded while the separators stay literal.
func encodeTrashPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// Trash stays on the selected mount; crossing mounts must never turn deletion into a copy.
func linuxTrashFor(path string) (string, string, error) {
	home, err := resolveLinuxTrashDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(home), 0700); err != nil {
		return "", "", err
	}
	entryDir, err := resolveMountDir(filepath.Dir(path))
	if err != nil {
		return "", "", err
	}
	homeDir, err := resolveMountDir(filepath.Dir(home))
	if err != nil {
		return "", "", err
	}
	same, err := sameMount(entryDir, homeDir)
	if err != nil {
		return "", "", err
	}
	if same {
		return home, path, nil
	}
	resolvedPath := filepath.Join(entryDir, filepath.Base(path))
	top, err := volumeTop(resolvedPath)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(top, resolvedPath)
	if err != nil {
		return "", "", err
	}
	return volumeTrashDir(top), relative, nil
}

// volumeTop resolves the enclosing mount point from /proc/self/mountinfo
// matched by STATX_MNT_ID, matching GIO/GVFS and KIO mount discovery.
func volumeTop(path string) (string, error) {
	dir, err := resolveMountDir(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	mntID, ok, err := statxMountID(dir)
	if err != nil {
		return "", err
	}
	if ok {
		if top, found := mountPointFromProc(mntID, dir); found {
			return top, nil
		}
	}
	return volumeTopWalk(dir)
}

func volumeTopWalk(dir string) (string, error) {
	top := dir
	for {
		parent := filepath.Dir(top)
		if parent == top {
			return top, nil
		}
		same, err := sameMount(top, parent)
		if err != nil {
			return "", err
		}
		if !same {
			return top, nil
		}
		top = parent
	}
}

// An administrator-created sticky `.Trash` directory holds one subdirectory per
// user; when there is none, or this user's subdirectory cannot be created or
// used, the user owns a `.Trash-<uid>` sibling.
func volumeTrashDir(top string) string {
	uid := os.Getuid()
	shared := filepath.Join(top, ".Trash")
	info, err := os.Lstat(shared) // #nosec G703 -- shared is the fixed .Trash name at the resolved mount root.
	if err == nil && info.IsDir() && info.Mode()&os.ModeSticky != 0 {
		perUser := filepath.Join(shared, fmt.Sprint(uid))
		if privateTrashDirectory(perUser) == nil {
			return perUser
		}
	}
	return filepath.Join(top, fmt.Sprintf(".Trash-%d", uid))
}

func privateTrashDirectory(path string) error {
	uid := int64(os.Getuid())
	if uid < 0 || uid > math.MaxUint32 {
		return fmt.Errorf("unsafe trash directory: %s", path)
	}
	mkdirErr := os.Mkdir(path, 0700)
	if mkdirErr != nil && !os.IsExist(mkdirErr) {
		return mkdirErr
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(uid) || info.Mode().Perm() != 0700 {
		return fmt.Errorf("unsafe trash directory: %s", path)
	}
	if mkdirErr == nil {
		parent, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		return errors.Join(fssync.File(parent), parent.Close())
	}
	return nil
}

func resolveLinuxTrashDir() (string, error) {
	if data := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(data) {
		return filepath.Join(data, "Trash"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "Trash"), nil
}

func removeTrashMetadata(receipt Receipt) error {
	expected := filepath.Join(filepath.Dir(filepath.Dir(receipt.Path)), "info", filepath.Base(receipt.Path)+".trashinfo")
	if receipt.Metadata != expected {
		return nil
	}
	err := os.Remove(expected)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
