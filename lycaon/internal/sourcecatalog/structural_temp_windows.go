//go:build windows

package sourcecatalog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func createStructuralTempFile(dir, pattern string) (*os.File, error) {
	prefix, suffix := structuralTempPatternParts(pattern)
	var lastErr error
	for range 128 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(nonce[:])+suffix)
		path, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return nil, err
		}
		handle, err := windows.CreateFile(path,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE,
			0,
		)
		if err == nil {
			return os.NewFile(uintptr(handle), name), nil
		}
		lastErr = err
		if !errors.Is(err, windows.ERROR_FILE_EXISTS) && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, err
		}
	}
	return nil, lastErr
}

func structuralTempPatternParts(pattern string) (string, string) {
	star := strings.LastIndex(pattern, "*")
	if star < 0 {
		return pattern, ""
	}
	return pattern[:star], pattern[star+1:]
}
