//go:build darwin || linux

package fseffect

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

// Remove unlinks a file or empty directory without following symlinks.
func Remove(req RemoveRequest) error {
	parent, err := openParent(req.Location, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	if handled, err := removeLinkAt(parent, req); handled || err != nil {
		return err
	}
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
	if err := unix.Unlinkat(parent.fd, parent.name, flags); err != nil {
		return err
	}
	return fsyncFD(parent.fd)
}

func removeRegularConditionally(parent *openedParent, req RemoveRequest) error {
	quarantine, err := unusedRemoveName(parent)
	if err != nil {
		return err
	}
	parentFD := parent.fd
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
		err := unix.Fstatat(parent.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect removal staging name: %w", err)
		}
	}
	return "", fmt.Errorf("stage removal: exhausted unique names")
}

// removeLinkAt handles the link entry under the held parent without opening
// its target. A regular entry remains with Remove's ordinary path.
func removeLinkAt(parent *openedParent, req RemoveRequest) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(parent.fd, parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFLNK {
		return false, nil
	}
	if req.BeforeCommit != nil {
		return true, fmt.Errorf("conditional link removal requires LinkSHA256")
	}
	if req.LinkSHA256 != "" {
		text, err := readLinkAt(parent.fd, parent.name)
		if err != nil {
			return true, err
		}
		digest := sha256.Sum256([]byte(text))
		if hex.EncodeToString(digest[:]) != req.LinkSHA256 {
			return true, fmt.Errorf("symbolic link changed before removal")
		}
	}
	if err := unix.Unlinkat(parent.fd, parent.name, 0); err != nil {
		return true, err
	}
	return true, fsyncFD(parent.fd)
}
