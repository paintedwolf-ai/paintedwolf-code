package testvolume

import (
	"io"
	"os"
)

// CopyAsClone simulates cloning for movable volumes without reflinks.
func CopyAsClone(src, dst string) (bool, error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return true, copyErr
}
