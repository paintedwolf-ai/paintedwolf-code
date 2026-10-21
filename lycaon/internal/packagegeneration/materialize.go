//go:build linux

package packagegeneration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
)

func materialize(staging *os.Root, raw []byte, store, locks string, owner uint32) (Generation, error) {
	if staging == nil {
		return Generation{}, errors.New("native generation: missing retained staging root")
	}
	rootInfo, err := staging.Stat(".")
	if err != nil {
		return Generation{}, err
	}
	if err := controlledObject(rootInfo, owner); err != nil {
		return Generation{}, err
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(raw))
	if err != nil {
		return Generation{}, err
	}
	if manifest.InstallRoot != "/opt/painted-wolf-code" {
		return Generation{}, errors.New("native generation: unsupported logical install root")
	}
	sum := sha256.Sum256(raw)
	identity := hex.EncodeToString(sum[:])
	selected := Generation{Root: filepath.Join(store, identity), ManifestSHA256: identity, LogicalInstallRoot: manifest.InstallRoot}
	for _, name := range []string{store, locks} {
		if err := controlledParents(name, owner); err != nil {
			return Generation{}, err
		}
		if err := os.MkdirAll(name, 0755); err != nil {
			return Generation{}, err
		}
		if err := controlledParents(name, owner); err != nil {
			return Generation{}, err
		}
	}
	lock, err := openLifetime(locks, identity, owner)
	if err != nil {
		return Generation{}, err
	}
	defer lock.Close()
	// Reinstalling a live identical generation must not wait for its users to exit.
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		return Generation{}, err
	}
	if _, err := os.Lstat(selected.Root); err == nil {
		return selected, verifyExisting(selected.Root, raw, manifest, owner)
	} else if !os.IsNotExist(err) {
		return Generation{}, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		return Generation{}, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return Generation{}, err
	}
	if _, err := os.Lstat(selected.Root); err == nil {
		return selected, verifyExisting(selected.Root, raw, manifest, owner)
	} else if !os.IsNotExist(err) {
		return Generation{}, err
	}
	if err := manifest.VerifyOwnedPayload(staging, owner); err != nil {
		return Generation{}, err
	}
	sourceManifest, err := readSeal(staging, len(raw), owner)
	if err != nil {
		return Generation{}, err
	}
	if !bytes.Equal(sourceManifest, raw) {
		return Generation{}, errors.New("native generation: staging seal differs from selection")
	}
	temporary, err := os.MkdirTemp(store, ".install-")
	if err != nil {
		return Generation{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(temporary)
		}
	}()
	target, err := os.OpenRoot(temporary)
	if err != nil {
		return Generation{}, err
	}
	defer target.Close()
	entries := append([]pkgmanifest.PayloadEntry(nil), manifest.Payload...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for _, entry := range entries {
		if err := copyEntry(staging, target, entry, owner); err != nil {
			return Generation{}, err
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Kind == "directory" {
			if err := target.Chmod(entries[index].Path, os.FileMode(entries[index].Mode)); err != nil {
				return Generation{}, err
			}
		}
	}
	if err := target.WriteFile("manifest.json", raw, 0644); err != nil {
		return Generation{}, err
	}
	if err := target.Chmod("manifest.json", 0644); err != nil {
		return Generation{}, err
	}
	if err := manifest.VerifyOwnedPayload(target, owner); err != nil {
		return Generation{}, err
	}
	if err := os.Chmod(temporary, 0755); err != nil {
		return Generation{}, err
	}
	if err := syncDirectory(temporary); err != nil {
		return Generation{}, err
	}
	if err := os.Rename(temporary, selected.Root); err != nil {
		return Generation{}, err
	}
	complete = true
	if err := syncDirectory(store); err != nil {
		return Generation{}, err
	}
	return selected, nil
}

func verifyExisting(path string, raw []byte, manifest *pkgmanifest.Manifest, owner uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("native generation: retained root is not physical")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := controlledObject(info, owner); err != nil {
		return err
	}
	actual, err := readSeal(root, len(raw), owner)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, raw) {
		return errors.New("native generation: retained seal differs")
	}
	return manifest.VerifyOwnedPayload(root, owner)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func copyEntry(source, target *os.Root, entry pkgmanifest.PayloadEntry, owner uint32) error {
	info, err := source.Lstat(entry.Path)
	if err != nil {
		return err
	}
	if err := controlledObject(info, owner); err != nil {
		return err
	}
	switch entry.Kind {
	case "directory":
		return target.Mkdir(entry.Path, 0700)
	case "symlink":
		actual, err := source.Readlink(entry.Path)
		if err != nil {
			return err
		}
		if actual != *entry.Target {
			return errors.New("native generation: source link changed")
		}
		return target.Symlink(actual, entry.Path)
	case "file":
		file, err := source.Open(entry.Path)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) || opened.Size() != *entry.Size {
			return errors.New("native generation: source image changed")
		}
		output, err := target.OpenFile(entry.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer output.Close()
		hasher := sha256.New()
		count, err := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(file, *entry.Size+1))
		if err != nil {
			return err
		}
		if count != *entry.Size || hex.EncodeToString(hasher.Sum(nil)) != *entry.SHA256 {
			return fmt.Errorf("native generation: source bytes changed for %q", entry.Path)
		}
		if err := output.Chmod(os.FileMode(entry.Mode)); err != nil {
			return err
		}
		return output.Sync()
	}
	return errors.New("native generation: unsupported payload object")
}

func readSeal(root *os.Root, size int, owner uint32) ([]byte, error) {
	info, err := root.Lstat("manifest.json")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(size) {
		return nil, errors.New("native generation: invalid bounded seal")
	}
	if err := controlledObject(info, owner); err != nil {
		return nil, err
	}
	file, err := root.Open("manifest.json")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("native generation: seal changed while opening")
	}
	return io.ReadAll(io.LimitReader(file, int64(size)+1))
}
