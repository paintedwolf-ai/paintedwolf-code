package projectpaths

import (
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// ErrIsDirectory reports a read aimed at a directory.
var ErrIsDirectory = errors.New("path is a directory")

// ErrNotRegular reports a read aimed at a special file.
var ErrNotRegular = errors.New("path is not a regular file")

// FileTooLargeError reports a file over the reader's bound. Size is unknown (-1) for
// compressed host data, which is refused as soon as decoding passes the bound.
type FileTooLargeError struct {
	Size  int64
	Limit int64
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("file exceeds %d bytes", e.Limit)
}

// ReadBounded reads a resolved file of at most limit bytes. Host data stored compressed,
// such as a prompt attachment, is returned decoded.
func (r Resolved) ReadBounded(limit int64) ([]byte, error) {
	return r.readBounded(limit, false)
}

// ReadRegularBounded reads at most limit bytes from a regular file.
func (r Resolved) ReadRegularBounded(limit int64) ([]byte, error) {
	return r.readBounded(limit, true)
}

func (r Resolved) readBounded(limit int64, regularOnly bool) ([]byte, error) {
	f, err := fseffect.OpenRead(r.EffectLocation())
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrIsDirectory
	}
	if regularOnly && !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if r.Compressed {
		raw, err := zstdcodec.ReadBounded(f, limit)
		if errors.Is(err, zstdcodec.ErrDecodedLimit) {
			return nil, &FileTooLargeError{Size: -1, Limit: limit}
		}
		return raw, err
	}
	if info.Size() > limit {
		return nil, &FileTooLargeError{Size: info.Size(), Limit: limit}
	}
	// The bounded read also catches a file that grows after the stat.
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, &FileTooLargeError{Size: int64(len(raw)), Limit: limit}
	}
	return raw, nil
}
