//go:build linux

package desktoptrash

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func platformMove(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	trashDir, err := resolveLinuxTrashDir()
	if err != nil {
		return err
	}

	filesDir := filepath.Join(trashDir, "files")
	infoDir := filepath.Join(trashDir, "info")

	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return fmt.Errorf("create trash files directory: %w", err)
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return fmt.Errorf("create trash info directory: %w", err)
	}

	baseName := filepath.Base(path)
	destName := baseName
	destFile := filepath.Join(filesDir, destName)
	destInfo := filepath.Join(infoDir, destName+".trashinfo")

	counter := 1
	for {
		_, fileErr := os.Lstat(destFile)
		_, infoErr := os.Lstat(destInfo)
		if os.IsNotExist(fileErr) && os.IsNotExist(infoErr) {
			break
		}
		destName = fmt.Sprintf("%s.%d", baseName, counter)
		destFile = filepath.Join(filesDir, destName)
		destInfo = filepath.Join(infoDir, destName+".trashinfo")
		counter++
	}

	// FreeDesktop trash spec: each entry in files/ has an info/<name>.trashinfo.
	now := time.Now().Format("2006-01-02T15:04:05")
	trashInfoContent := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n", url.PathEscape(path), now)
	if err := os.WriteFile(destInfo, []byte(trashInfoContent), 0o600); err != nil {
		return fmt.Errorf("write trash metadata: %w", err)
	}

	if err := os.Rename(path, destFile); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			// The trash is on another filesystem, so rename cannot move it.
			if copyErr := moveCrossDevice(path, destFile, info); copyErr != nil {
				_ = os.Remove(destInfo)
				return copyErr
			}
		} else {
			_ = os.Remove(destInfo)
			return fmt.Errorf("move to trash: %w", err)
		}
	}

	return nil
}

func resolveLinuxTrashDir() (string, error) {
	xdgDataHome := os.Getenv("XDG_DATA_HOME")
	if xdgDataHome != "" && filepath.IsAbs(xdgDataHome) {
		return filepath.Join(xdgDataHome, "Trash"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "Trash"), nil
}

func moveCrossDevice(src, dst string, info os.FileInfo) error {
	if info.IsDir() {
		if err := copyDir(src, dst); err != nil {
			_ = os.RemoveAll(dst)
			return err
		}
		return os.RemoveAll(src)
	}
	if err := copyFile(src, dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	info, err := in.Stat()
	if err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}
