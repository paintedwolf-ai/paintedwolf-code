//go:build linux

package packagegeneration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

// Select holds the lifetime lock across the protected caller's atomic policy commit.
// commit is fixed trusted root code; no artifact or command argument supplies it.
func Select(identity string, commit func(Generation) error) error {
	if os.Geteuid() != 0 {
		return errors.New("native generation: selection requires root")
	}
	return selectGeneration(identity, commit, nativeStore, lifetimeLocks, 0)
}

func selectGeneration(identity string, commit func(Generation) error, store, locks string, owner uint32) error {
	if commit == nil {
		return errors.New("native generation: missing protected policy writer")
	}
	lock, err := openLifetime(locks, identity, owner)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		return err
	}
	root, raw, manifest, err := retainedGeneration(store, identity, owner)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := verifyExisting(filepath.Join(store, identity), raw, manifest, owner); err != nil {
		return err
	}
	return commit(Generation{Root: filepath.Join(store, identity), ManifestSHA256: identity, LogicalInstallRoot: manifest.InstallRoot})
}

// Reclaim never blocks a running generation. The fixed policy reader is called
// under the exclusive lock; selection takes the matching shared lock.
func Reclaim(identity string, freshSelection func() (string, error)) (bool, error) {
	if os.Geteuid() != 0 {
		return false, errors.New("native generation: reclamation requires root")
	}
	return reclaim(identity, freshSelection, nativeStore, lifetimeLocks, 0)
}

func reclaim(identity string, freshSelection func() (string, error), store, locks string, owner uint32) (bool, error) {
	if freshSelection == nil {
		return false, errors.New("native generation: missing protected selection reader")
	}
	lock, err := openLifetime(locks, identity, owner)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	selected, err := freshSelection()
	if err != nil {
		return false, err
	}
	path := filepath.Join(store, identity)
	if selected == path || strings.HasPrefix(selected, path+string(os.PathSeparator)) {
		return false, nil
	}
	root, raw, manifest, err := retainedGeneration(store, identity, owner)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	if err := verifyExisting(path, raw, manifest, owner); err != nil {
		return false, err
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, syncDirectory(store)
}

func retainedGeneration(store, identity string, owner uint32) (*os.Root, []byte, *pkgmanifest.Manifest, error) {
	path := filepath.Join(store, identity)
	if err := controlledParents(path, owner); err != nil {
		return nil, nil, nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	info, err := root.Lstat("manifest.json")
	if err != nil {
		return nil, nil, nil, err
	}
	if info.Size() < 0 || info.Size() > 4<<20 {
		return nil, nil, nil, errors.New("native generation: oversized retained seal")
	}
	raw, err := readSeal(root, int(info.Size()), owner)
	if err != nil {
		return nil, nil, nil, err
	}
	manifest, err := pkgmanifest.Read(strings.NewReader(string(raw)))
	if err != nil {
		return nil, nil, nil, err
	}
	if manifest.InstallRoot != "/opt/painted-wolf-code" {
		return nil, nil, nil, errors.New("native generation: unexpected logical root")
	}
	if digest(raw) != identity {
		return nil, nil, nil, errors.New("native generation: directory differs from complete seal")
	}
	failed = false
	return root, raw, manifest, nil
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
