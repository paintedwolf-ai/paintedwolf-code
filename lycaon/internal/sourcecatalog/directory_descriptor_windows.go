//go:build windows

package sourcecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"golang.org/x/sys/windows"
)

func openDirectoryFile(root *os.Root, name string) (*os.File, error) {
	target, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !target.IsDir() {
		return nil, os.ErrInvalid
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(target, opened) {
		_ = file.Close()
		if err == nil {
			err = os.ErrNotExist
		}
		return nil, err
	}
	return file, nil
}

func openStructuralScanDirectoryFile(root *os.Root, name string) (*os.File, error) {
	clean := filepath.ToSlash(filepath.Clean(name))
	if filepath.IsAbs(name) || sandbox.HasParentTraversal(clean) {
		return nil, os.ErrPermission
	}
	if clean == "" {
		clean = "."
	}
	if clean != "." {
		current := ""
		for _, component := range strings.Split(clean, "/") {
			if component == "" || component == "." || component == ".." {
				return nil, os.ErrPermission
			}
			if current == "" {
				current = component
			} else {
				current = current + "/" + component
			}
			info, err := root.Lstat(filepath.FromSlash(current))
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, os.ErrInvalid
			}
		}
	}
	file, err := openDirectoryFile(root, filepath.FromSlash(clean))
	if err != nil {
		return nil, err
	}
	if err := verifyStructuralScanDirectoryHandle(root, clean, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func verifyStructuralScanDirectoryHandle(root *os.Root, clean string, file *os.File) error {
	finalPath, err := structuralScanFinalPath(file)
	if err != nil {
		return err
	}
	rootFile, err := openDirectoryFile(root, ".")
	if err != nil {
		return err
	}
	rootPath, err := structuralScanFinalPath(rootFile)
	_ = rootFile.Close()
	if err != nil {
		return err
	}
	rootPath = normalizeStructuralScanWindowsPath(rootPath)
	expected := normalizeStructuralScanWindowsPath(filepath.Join(rootPath, filepath.FromSlash(clean)))
	finalPath = normalizeStructuralScanWindowsPath(finalPath)
	if repochange.IsPrivatePath(finalPath) {
		return os.ErrPermission
	}
	if !strings.EqualFold(finalPath, expected) {
		return os.ErrInvalid
	}
	return nil
}

func structuralScanFinalPath(file *os.File) (string, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return "", err
	}
	var finalPath string
	var finalErr error
	err = conn.Control(func(original uintptr) {
		finalPath, finalErr = structuralScanFinalPathHandle(windows.Handle(original))
	})
	if err != nil {
		return "", err
	}
	return finalPath, finalErr
}

func structuralScanFinalPathHandle(handle windows.Handle) (string, error) {
	size := uint32(512)
	for {
		buffer := make([]uint16, size)
		written, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if written < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:written]), nil
		}
		size = written + 1
	}
}

func normalizeStructuralScanWindowsPath(name string) string {
	const longUNC = `\\?\UNC\`
	const longPath = `\\?\`
	if strings.HasPrefix(name, longUNC) {
		name = `\\` + strings.TrimPrefix(name, longUNC)
	} else if strings.HasPrefix(name, longPath) {
		name = strings.TrimPrefix(name, longPath)
	}
	return filepath.Clean(name)
}

func structuralScanDirectoryCacheSupported() bool { return false }

func openStructuralScanCacheRootFile(*os.Root) (*os.File, error) {
	return nil, errStructuralScanDirectoryCacheUnsupported
}

func duplicateStructuralScanDirectoryFile(file *os.File) (*os.File, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var duplicate windows.Handle
	var duplicateErr error
	if err := conn.Control(func(original uintptr) {
		process := windows.CurrentProcess()
		duplicateErr = windows.DuplicateHandle(process, windows.Handle(original), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS)
	}); err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	return os.NewFile(uintptr(duplicate), file.Name()), nil
}

func openStructuralScanChildDirectoryFile(*os.File, string) (*os.File, error) {
	return nil, errStructuralScanDirectoryCacheUnsupported
}

func structuralScanPathMatchesOpenDirectory(*os.File, string, *os.File) (bool, error) {
	return false, errStructuralScanDirectoryCacheUnsupported
}

func normalizeStructuralScanDirectoryOpenError(err error) error { return err }

func directoryKinds(file *os.File) (*directoryKindReader, error) {
	duplicate, err := duplicateStructuralScanDirectoryFile(file)
	if err != nil {
		return nil, err
	}
	return &directoryKindReader{file: duplicate}, nil
}

// Windows keeps no change time; the creation time tells a recreated directory
// from the one whose listing was recorded.
func directoryStampOf(info os.FileInfo) DirectoryStamp {
	stamp := DirectoryStamp{Modified: info.ModTime().UnixNano()}
	if attributes, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		stamp.Changed = attributes.CreationTime.Nanoseconds()
	}
	return stamp
}
