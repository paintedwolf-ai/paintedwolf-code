package sourceview

import (
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// Access selects the byte budget and rejection codes for one file load.
type Access uint8

const (
	// AccessRead loads within readcaps.MaxFileBytes.
	AccessRead Access = iota
	// AccessMutate loads a mutation base within readcaps.MaxMutationBytes, the
	// bound that retained versions, checkpoints, and editor documents share.
	AccessMutate
)

// MaxBytes is the raw byte budget for a project file loaded with this access.
func (a Access) MaxBytes() int64 {
	if a == AccessMutate {
		return readcaps.MaxMutationBytes
	}
	return readcaps.MaxFileBytes
}

// SizeReject reports a file or proposed content above maxBytes. size is the
// measured lower bound; zero means a decoder stopped before measuring.
func (a Access) SizeReject(tool, path string, size, maxBytes int64) error {
	code := "READ_FILE_TOO_LARGE"
	if a == AccessMutate {
		code = "EDIT_FILE_TOO_LARGE"
	}
	data := map[string]any{"tool": tool, "path": path, "max_file_bytes": maxBytes}
	if size > 0 {
		data["size"] = size
	}
	return &tools.ToolReject{Code: code, Data: data}
}

// MutationSizeReject reports mutation content above readcaps.MaxMutationBytes.
func MutationSizeReject(tool, path string, size int64) error {
	return AccessMutate.SizeReject(tool, path, size, readcaps.MaxMutationBytes)
}

// ReadContentCapped loads one bounded regular file. compressed marks a
// resolved path that blobstore.Store wrote as a zstd frame — set only from
// projectpaths.Resolved.Compressed, a directory-scoped fact, never from
// sniffing the file's bytes.
func ReadContentCapped(tool string, access Access, loc fseffect.Location, displayPath string, compressed bool, maxBytes int64) ([]byte, error) {
	f, err := fseffect.OpenRead(loc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, &tools.ToolReject{
			Code: "READ_IS_DIRECTORY",
			Data: map[string]any{"path": displayPath},
		}
	}
	if compressed {
		return readCompressedContentCapped(tool, access, f, displayPath, maxBytes)
	}
	if info.Size() > maxBytes {
		return nil, access.SizeReject(tool, displayPath, info.Size(), maxBytes)
	}
	return readPlainContentCapped(tool, access, f, displayPath, maxBytes)
}

func readPlainContentCapped(tool string, access Access, r io.Reader, path string, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if int64(len(body)) > maxBytes {
		return nil, access.SizeReject(tool, path, int64(len(body)), maxBytes)
	}
	if err != nil {
		return nil, err
	}
	return body, nil
}

func readCompressedContentCapped(tool string, access Access, r io.Reader, path string, maxBytes int64) ([]byte, error) {
	body, err := zstdcodec.ReadBounded(r, maxBytes)
	if errors.Is(err, zstdcodec.ErrDecodedLimit) {
		return nil, access.SizeReject(tool, path, 0, maxBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("decompress body: %w", err)
	}
	return body, nil
}
