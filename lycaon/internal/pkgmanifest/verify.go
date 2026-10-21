package pkgmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// SizeMismatchError reports descriptor bytes whose size differs from the entry.
type SizeMismatchError struct {
	Path      string
	Want, Got int64
}

func (e *SizeMismatchError) Error() string {
	return fmt.Sprintf("native manifest: %s has size %d, want %d", e.Path, e.Got, e.Want)
}

// Verify hashes the file at path and compares it with its manifest entry.
func (m *Manifest) Verify(path string) error {
	want, ok := m.Lookup(path)
	if !ok {
		return &UnlistedError{Path: path}
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return VerifyOpened(file, want)
}

// VerifyOpened compares retained file bytes with one validated manifest entry.
// It neither resolves the entry's path nor changes the file offset or lifetime.
// Root ownership, role authorization and executable dispatch remain caller-owned.
func VerifyOpened(file *os.File, want Executable) error {
	if !isCleanAbsolute(want.Path) || want.Size < 0 || !isLowerHexDigest(want.SHA256) {
		return errors.New("native manifest: invalid executable expectation")
	}
	if file == nil {
		return errors.New("native manifest: missing opened executable")
	}
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("native manifest: opened executable is not a regular file")
	}
	if before.Size() != want.Size {
		return &SizeMismatchError{Path: want.Path, Want: want.Size, Got: before.Size()}
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(file, 0, before.Size()))
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("native manifest: opened executable %s changed during verification", want.Path)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if n != want.Size || got != want.SHA256 {
		return &MismatchError{Path: want.Path, Want: want.SHA256, Got: got}
	}
	return nil
}
