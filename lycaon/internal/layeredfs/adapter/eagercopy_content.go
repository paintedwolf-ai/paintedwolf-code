package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync/atomic"
)

// contentOpen is the one door through which a materialized path reads file content, so a clone
// preparation and an unchanged file at capture can be shown to read nothing.
var contentOpen = func(path string) (*os.File, error) { return os.Open(path) }

// contentMeter counts content bytes read through the door.
type contentMeter struct{ read atomic.Int64 }

func (m *contentMeter) hashFile(path string) (string, int64, error) {
	f, err := contentOpen(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	m.read.Add(n)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (m *contentMeter) readFile(path string) ([]byte, error) {
	f, err := contentOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	m.read.Add(int64(len(raw)))
	return raw, err
}

// copyFile streams at most limit bytes from src into a new dst when limit is positive.
func (m *contentMeter) copyFile(src, dst string, mode os.FileMode, limit int64) (int64, error) {
	in, err := contentOpen(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	var reader io.Reader = in
	if limit > 0 {
		reader = io.LimitReader(in, limit)
	}
	n, copyErr := io.Copy(out, reader)
	m.read.Add(n)
	return n, errors.Join(copyErr, out.Close())
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
