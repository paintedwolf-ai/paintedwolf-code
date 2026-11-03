package sourceview

import (
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// FileStatFacts captures the open file descriptor's metadata at the instant of read.
type FileStatFacts struct {
	Size, ModTimeNS int64
	Ino, Dev        uint64
}

func extractFileStatFacts(info os.FileInfo) FileStatFacts {
	if info == nil {
		return FileStatFacts{}
	}
	facts := FileStatFacts{Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
		facts.Ino = uint64(sys.Ino)
		facts.Dev = uint64(sys.Dev)
	}
	return facts
}

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
	return &toolrejection.ToolReject{Code: code, Data: data}
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
	body, _, err := ReadContentWithStatCapped(tool, access, loc, displayPath, compressed, maxBytes)
	return body, err
}

// ReadContentWithStatCapped loads one bounded regular file along with the open descriptor's stat facts.
func ReadContentWithStatCapped(tool string, access Access, loc fseffect.Location, displayPath string, compressed bool, maxBytes int64) ([]byte, FileStatFacts, error) {
	f, err := fseffect.OpenRead(loc)
	if err != nil {
		return nil, FileStatFacts{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, FileStatFacts{}, err
	}
	if info.IsDir() {
		return nil, FileStatFacts{}, DirectoryReject(displayPath)
	}
	stat := extractFileStatFacts(info)
	if compressed {
		body, cErr := readCompressedContentCapped(tool, access, f, displayPath, maxBytes)
		stat.Size = int64(len(body))
		return body, stat, cErr
	}
	if info.Size() > maxBytes {
		return nil, stat, access.SizeReject(tool, displayPath, info.Size(), maxBytes)
	}
	body, rErr := readPlainContentCapped(tool, access, f, displayPath, maxBytes)
	stat.Size = int64(len(body))
	return body, stat, rErr
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
