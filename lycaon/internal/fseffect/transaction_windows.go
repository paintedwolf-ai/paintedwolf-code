//go:build windows

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
	"unsafe"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"

	"golang.org/x/sys/windows"
)

const windowsShare = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE

type windowsParent struct {
	handle windows.Handle
	path   string
	name   string
}

func (p *windowsParent) close() { _ = windows.CloseHandle(p.handle) }

type windowsTarget struct {
	parent *windowsParent
	name   string
}

type windowsHandleTarget struct {
	handle windows.Handle
	path   string
}

func (t windowsHandleTarget) Open() (*os.File, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, t.handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(duplicate), t.path), nil
}

func (t windowsHandleTarget) Lstat() (os.FileInfo, error) {
	f, err := t.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}

func (t windowsTarget) Open() (*os.File, error) {
	return openWindowsFile(t.parent, t.name, windows.FILE_GENERIC_READ, windows.FILE_OPEN, 0)
}

func (t windowsTarget) Lstat() (os.FileInfo, error) {
	f, err := t.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}

func openWindowsParent(loc Location, create, write bool) (*windowsParent, error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(clean.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve effect root: %w", err)
	}
	handle, err := openWindowsRoot(root, write)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.FILE_GENERIC_READ)
	if write {
		access |= windows.FILE_GENERIC_WRITE
	}
	parts := splitWindowsRel(clean.Rel)
	currentPath := root
	for _, component := range parts[:len(parts)-1] {
		next, openErr := ntOpenRelative(handle, component,
			access, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
		created := false
		if openErr != nil && create && os.IsNotExist(openErr) {
			next, openErr = ntOpenRelative(handle, component,
				access, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_WRITE_THROUGH)
			created = openErr == nil
		}
		if created {
			if syncErr := flushHandle(handle); syncErr != nil {
				_ = windows.CloseHandle(next)
				_ = windows.CloseHandle(handle)
				return nil, syncErr
			}
		}
		_ = windows.CloseHandle(handle)
		if openErr != nil {
			// PathError so os.IsNotExist recognizes a missing component.
			return nil, &fs.PathError{Op: "open effect directory", Path: component, Err: openErr}
		}
		handle = next
		currentPath = filepath.Join(currentPath, component)
	}
	return &windowsParent{handle: handle, path: currentPath, name: parts[len(parts)-1]}, nil
}

func openWindowsRoot(path string, write bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(windowsNTPath(path))
	if err != nil {
		return 0, err
	}
	access := uint32(windows.FILE_GENERIC_READ)
	if write {
		access |= windows.FILE_GENERIC_WRITE
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:     uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	return ntCreate(&oa, access, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
}

func windowsNTPath(path string) string {
	if strings.HasPrefix(path, `\\`) {
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\??\` + path
}

func splitWindowsRel(rel string) []string {
	return strings.Split(filepath.Clean(rel), string(filepath.Separator))
}

func ntOpenRelative(parent windows.Handle, name string, access, disposition, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	return ntCreate(&oa, access, disposition, options)
}

func ntCreate(oa *windows.OBJECT_ATTRIBUTES, access, disposition, options uint32) (windows.Handle, error) {
	return ntCreateShared(oa, access, disposition, options, windowsShare)
}

func ntCreateShared(oa *windows.OBJECT_ATTRIBUTES, access, disposition, options, share uint32) (windows.Handle, error) {
	return ntCreateEntry(oa, access, disposition, options, share, false)
}

// A single leaf may be a link; its held parent has already been verified.
func ntOpenLeaf(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	if name == "." || name == ".." || name == "" || strings.ContainsAny(name, `/\`+`:`) {
		return 0, ErrSymlink
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE,
	}
	return ntCreateEntry(&oa, access, windows.FILE_OPEN, 0, windowsShare, true)
}

func ntCreateEntry(oa *windows.OBJECT_ATTRIBUTES, access, disposition, options, share uint32, allowLeafLink bool) (windows.Handle, error) {
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(&handle, access, oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL,
		share, disposition, options|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			if status == windows.STATUS_REPARSE_POINT_ENCOUNTERED {
				return 0, ErrSymlink
			}
			return 0, status.Errno()
		}
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && !allowLeafLink {
		_ = windows.CloseHandle(handle)
		return 0, ErrSymlink
	}
	return handle, nil
}

func ntOpenRelativeShared(parent windows.Handle, name string, access, disposition, options, share uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	return ntCreateShared(&oa, access, disposition, options, share)
}

func openWindowsFile(parent *windowsParent, name string, access, disposition, options uint32) (*os.File, error) {
	handle, err := ntOpenRelative(parent.handle, name, access, disposition, options|windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(parent.path, name)), nil
}

// OpenWrite opens command output relative to a held, non-reparse parent.
func OpenWrite(loc Location, appendMode bool, mode os.FileMode) (*os.File, error) {
	parent, err := openWindowsParent(loc, true, true)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	f, err := openWindowsFile(parent, parent.name,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF, 0)
	if err != nil {
		return nil, err
	}
	if appendMode {
		_, err = f.Seek(0, io.SeekEnd)
	} else {
		err = f.Truncate(0)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	_ = f.Chmod(mode.Perm())
	return f, nil
}

func platformReplace(req ReplaceRequest) (Result, error) {
	return replace(req, nil)
}

func replace(req ReplaceRequest, inject func(stage) error) (Result, error) {
	if req.Source == nil {
		return Result{}, fmt.Errorf("atomic filesystem effect request is incomplete")
	}
	parent, err := openWindowsParent(req.Location, true, true)
	if err != nil {
		return Result{}, err
	}
	defer parent.close()
	tmpName, tmp, held, err := createWindowsTemp(parent)
	if err != nil {
		return Result{}, err
	}
	defer held.retire()
	committed := false
	defer func() {
		if !committed {
			_ = deleteWindowsHandle(windows.Handle(tmp.Fd()))
		}
		_ = tmp.Close()
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
		if err := req.ReviewStaged(windowsTarget{parent: parent, name: parent.name}, result); err != nil {
			return result, err
		}
	}
	unlock := lockTarget(req.Location)
	defer unlock()
	mode, err := req.resolvedMode(windowsTarget{parent: parent, name: parent.name})
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
	// The staged handle remains open across rename.
	if err := injectAt(inject, stageFileSynced); err != nil {
		return result, err
	}
	if err := injectAt(inject, stageFileClosed); err != nil {
		return result, err
	}
	if err := req.precondition(windowsTarget{parent: parent, name: parent.name}, result); err != nil {
		return result, err
	}
	if err := injectAt(inject, stageValidated); err != nil {
		return result, err
	}
	if req.ReviewStaged != nil {
		if err := verifyWindowsTarget(windowsTarget{parent: parent, name: tmpName}, result); err != nil {
			return result, fmt.Errorf("reviewed staging changed: %w", err)
		}
	}
	if err := renameWindowsHandle(windows.Handle(tmp.Fd()), parent.handle, parent.name); err != nil {
		return result, fmt.Errorf("commit replacement: %w", err)
	}
	committed = true
	if err := injectAt(inject, stageRenamed); err != nil {
		return result, err
	}
	if err := flushHandle(parent.handle); err != nil {
		return result, fmt.Errorf("sync replacement directory: %w", err)
	}
	if err := injectAt(inject, stageDirSynced); err != nil {
		return result, err
	}
	if err := verifyWindowsTarget(windowsTarget{parent: parent, name: parent.name}, result); err != nil {
		return result, err
	}
	return result, injectAt(inject, stageVerified)
}

// createWindowsTemp holds the staging name before creating it so no listing
// sees the entry unregistered; the caller retires the hold after it is gone.
func createWindowsTemp(parent *windowsParent) (string, *os.File, stagingHold, error) {
	directory, err := fspath.EntryIdentityHandle(parent.handle)
	if err != nil {
		return "", nil, stagingHold{}, fmt.Errorf("identify replacement parent: %w", err)
	}
	for range 64 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", nil, stagingHold{}, err
		}
		name := "." + parent.name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
		held := holdStaging(directory, name)
		handle, err := ntOpenRelative(parent.handle, name,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE,
			windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_WRITE_THROUGH)
		if err != nil {
			held.abandon()
		}
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return "", nil, stagingHold{}, err
		}
		return name, os.NewFile(uintptr(handle), filepath.Join(parent.path, name)), held, nil
	}
	return "", nil, stagingHold{}, fmt.Errorf("create replacement: exhausted unique names")
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameWindowsHandle(handle, parent windows.Handle, name string) error {
	return renameWindowsEntry(handle, parent, name, true)
}

func renameWindowsEntry(handle, parent windows.Handle, name string, replace bool) error {
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameBytes := (len(encoded) - 1) * 2
	var layout fileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+nameBytes)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	}
	info.RootDirectory = parent
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(encoded)-1), encoded[:len(encoded)-1])
	var iosb windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(handle, &iosb, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			return status.Errno()
		}
		return err
	}
	return nil
}

func deleteWindowsHandle(handle windows.Handle) error {
	delete := byte(1)
	var iosb windows.IO_STATUS_BLOCK
	err := windows.NtSetInformationFile(handle, &iosb, &delete, 1, windows.FileDispositionInformation)
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}

func verifyWindowsTarget(target windowsTarget, want Result) error {
	f, err := target.Open()
	if err != nil {
		return fmt.Errorf("%w: open destination: %v", ErrPostcondition, err)
	}
	defer func() { _ = f.Close() }()
	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil || n != want.Bytes || hex.EncodeToString(hasher.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%w: destination bytes differ", ErrPostcondition)
	}
	return nil
}

// Remove deletes a target by handle without reopening an absolute path.
func Remove(req RemoveRequest) error {
	parent, err := openWindowsParent(req.Location, false, true)
	if err != nil {
		return err
	}
	defer parent.close()
	share := uint32(windows.FILE_SHARE_READ)
	access := uint32(windows.DELETE | windows.FILE_READ_ATTRIBUTES)
	if req.BeforeCommit == nil {
		share = windowsShare
	} else {
		access |= windows.FILE_GENERIC_READ
	}
	handle, err := ntOpenRelativeShared(parent.handle, parent.name,
		access, windows.FILE_OPEN, 0, share)
	if err != nil {
		return err
	}
	if req.BeforeCommit != nil {
		target := windowsHandleTarget{handle: handle, path: filepath.Join(parent.path, parent.name)}
		info, statErr := target.Lstat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = windows.CloseHandle(handle)
			return fmt.Errorf("conditional remove requires a regular file")
		}
		if err := req.BeforeCommit(target); err != nil {
			_ = windows.CloseHandle(handle)
			return err
		}
	}
	if err := deleteWindowsHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	if err := windows.CloseHandle(handle); err != nil {
		return err
	}
	return flushHandle(parent.handle)
}

// Rename moves a target through held source and destination parent handles.
func Rename(root, fromRel, toRel string) error {
	return renameEntry(root, fromRel, toRel, true, "")
}

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
	from, err := openWindowsParent(source, false, true)
	if err != nil {
		return err
	}
	defer from.close()
	to, err := openWindowsParent(destination, true, true)
	if err != nil {
		return err
	}
	defer to.close()
	handle, err := ntOpenLeaf(from.handle, from.name, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if expected != "" {
		identity, err := fspath.EntryIdentityHandle(handle)
		if err != nil {
			return err
		}
		if identity != expected {
			return ErrPostcondition
		}
	}
	if err := renameWindowsEntry(handle, to.handle, to.name, replace); err != nil {
		return err
	}
	if err := flushHandle(from.handle); err != nil {
		return err
	}
	return flushHandle(to.handle)
}

// UpdateMode applies a guarded permission transition without reparse traversal.
func UpdateMode(req ModeUpdateRequest) (ModeUpdateResult, error) {
	unlock := lockTarget(req.Location)
	defer unlock()
	parent, err := openWindowsParent(req.Location, false, true)
	if err != nil {
		return ModeUpdateResult{}, err
	}
	defer parent.close()
	handle, err := ntOpenRelative(parent.handle, parent.name,
		windows.FILE_GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_OPEN, 0)
	if err != nil {
		return ModeUpdateResult{}, err
	}
	f := os.NewFile(uintptr(handle), filepath.Join(parent.path, parent.name))
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ModeUpdateResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ModeUpdateResult{}, ErrInvalidPath
	}
	if req.BeforeCommit != nil {
		if err := req.BeforeCommit(f, info); err != nil {
			return ModeUpdateResult{}, err
		}
	}
	result, appliedMode := resolveModeUpdate(info.Mode(), req.Update)
	if result.After != result.Before {
		if err := f.Chmod(appliedMode); err != nil {
			return ModeUpdateResult{}, err
		}
	}
	return result, nil
}

// Chown is unsupported on this platform.
func Chown(Location, int, int) error { return ErrUnsupported }

// MkdirAll creates and flushes directories through held handles.
func MkdirAll(loc Location, _ os.FileMode) error {
	clean, err := cleanLocation(loc)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(clean.Root)
	if err != nil {
		return err
	}
	handle, err := openWindowsRoot(root, true)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	anyCreated := false
	for _, component := range splitWindowsRel(clean.Rel) {
		next, err := ntOpenRelative(handle, component,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
		created := false
		if err != nil && os.IsNotExist(err) {
			next, err = ntOpenRelative(handle, component,
				windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
				windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_WRITE_THROUGH)
			created = err == nil
		}
		if err != nil {
			return err
		}
		if created {
			anyCreated = true
			if err := flushHandle(handle); err != nil {
				_ = windows.CloseHandle(next)
				return err
			}
		}
		_ = windows.CloseHandle(handle)
		handle = next
	}
	if !anyCreated {
		return nil
	}
	return flushHandle(handle)
}
