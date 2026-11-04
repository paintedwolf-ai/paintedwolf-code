//go:build windows

package fseffect

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/windows"
)

func openGuardedDirectory(loc Location, expected string) (*os.File, error) {
	parent, err := openWindowsParent(loc, false, true)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	handle, err := ntOpenRelative(parent.handle, parent.name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(handle), parent.name)
	identity, err := fspath.EntryIdentityHandle(handle)
	if err == nil && !fspath.SameEntryAcrossRestart(expected, identity) {
		err = ErrPostcondition
	}
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return dir, nil
}

func platformHardLink(req HardLinkRequest) error {
	src, err := cleanLocation(req.Source)
	if err != nil {
		return err
	}
	dst, err := cleanLocation(req.Location)
	if err != nil {
		return err
	}
	unlock := lockTarget(dst)
	defer unlock()

	srcParent, err := openWindowsParent(src, false, false)
	if err != nil {
		return err
	}
	defer srcParent.close()

	srcHandle, err := ntOpenRelative(srcParent.handle, srcParent.name,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(srcHandle) }()

	dstParent, err := openWindowsParent(dst, req.DirMode != 0, true)
	if err != nil {
		return err
	}
	defer dstParent.close()

	dstTarget := windowsTarget{parent: dstParent, name: dstParent.name}
	if _, statErr := dstTarget.Lstat(); statErr == nil && !req.Replace {
		return os.ErrExist
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	tmpName, held, err := stageWindowsHardLink(srcHandle, dstParent)
	if err != nil {
		return err
	}
	defer func() {
		if h, openErr := ntOpenRelative(dstParent.handle, tmpName, windows.DELETE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE); openErr == nil {
			_ = deleteWindowsHandle(h)
			_ = windows.CloseHandle(h)
		}
		held.retire()
	}()

	if req.BeforeCommit != nil {
		if err := req.BeforeCommit(dstTarget); err != nil {
			return err
		}
	}
	stagedHandle, err := ntOpenRelative(dstParent.handle, tmpName,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(stagedHandle) }()
	if err := renameWindowsEntry(stagedHandle, dstParent.handle, dstParent.name, req.Replace); err != nil {
		return err
	}
	return flushHandle(dstParent.handle)
}

func stageWindowsHardLink(srcHandle windows.Handle, dstParent *windowsParent) (string, stagingHold, error) {
	directory, err := fspath.EntryIdentityHandle(dstParent.handle)
	if err != nil {
		return "", stagingHold{}, fmt.Errorf("identify hard link parent: %w", err)
	}
	for range 64 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", stagingHold{}, err
		}
		name := "." + dstParent.name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
		held := holdStaging(directory, name)
		err := linkWindowsHandle(srcHandle, dstParent.handle, name)
		if err != nil {
			held.abandon()
			if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return "", stagingHold{}, err
		}
		return name, held, nil
	}
	return "", stagingHold{}, fmt.Errorf("create hard link: exhausted unique names")
}

func linkWindowsHandle(srcHandle, dstParent windows.Handle, name string) error {
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameBytes := (len(encoded) - 1) * 2
	var layout fileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+nameBytes)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = dstParent
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(encoded)-1), encoded[:len(encoded)-1])
	var iosb windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(srcHandle, &iosb, &buffer[0], uint32(len(buffer)), windows.FileLinkInformation); err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			return status.Errno()
		}
		return err
	}
	return nil
}
