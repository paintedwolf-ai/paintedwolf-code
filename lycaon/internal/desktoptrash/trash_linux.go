//go:build linux

package desktoptrash

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
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
	info, err := os.OpenFile(infoPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Receipt{}, err
	}
	_, writeErr := fmt.Fprintf(info, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", url.PathEscape(origin), time.Now().Format("2006-01-02T15:04:05"))
	if writeErr == nil {
		writeErr = fssync.File(info)
	}
	if err := errors.Join(writeErr, info.Close()); err != nil {
		_ = os.Remove(infoPath)
		return Receipt{}, err
	}
	directory, err := os.Open(metadata)
	if err != nil {
		return Receipt{}, err
	}
	syncErr := errors.Join(fssync.File(directory), directory.Close())
	if syncErr != nil {
		return Receipt{}, syncErr
	}
	receipt := Receipt{Path: filepath.Join(files, name), Metadata: infoPath}
	err = fseffect.RelocateGuarded(fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, fseffect.Location{Root: files, Rel: name}, identity)
	if err != nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			_ = os.Remove(infoPath)
		}
		return receipt, err
	}
	return receipt, nil
}

// Trash stays on the selected volume; crossing devices must never turn deletion into a copy.
func linuxTrashFor(path string) (string, string, error) {
	home, err := resolveLinuxTrashDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(home), 0700); err != nil {
		return "", "", err
	}
	same, err := fspath.SameFilesystem(path, filepath.Dir(home))
	if err != nil {
		return "", "", err
	}
	if same {
		return home, path, nil
	}
	top := filepath.Dir(path)
	for {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		same, err := fspath.SameFilesystem(top, parent)
		if err != nil {
			return "", "", err
		}
		if !same {
			break
		}
		top = parent
	}
	relative, err := filepath.Rel(top, path)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(top, fmt.Sprintf(".Trash-%d", os.Getuid())), relative, nil
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
