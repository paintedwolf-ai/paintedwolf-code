package sourceview

import (
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
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
	// AccessCommand bounds operations that genuinely need a complete command input.
	AccessCommand
)

// MaxBytes is the raw byte budget for a project file loaded with this access.
func (a Access) MaxBytes() int64 {
	if a == AccessCommand {
		catalog, err := toolcontract.LanguageV1()
		if err != nil {
			return 0
		}
		for _, limit := range catalog.Language.Limits {
			if limit.ID == "whole_file_bytes" {
				return limit.Maximum
			}
		}
		return 0
	}
	if a == AccessMutate {
		return readcaps.MaxMutationBytes
	}
	return readcaps.MaxFileBytes
}

// SizeReject reports a file or proposed content above maxBytes. size is the
// measured lower bound; zero means a decoder stopped before measuring.
func (a Access) SizeReject(tool, path string, size, maxBytes int64) error {
	if a == AccessCommand {
		return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"command": tool, "path": path, "limit_id": "whole_file_bytes", "resource_unit": "bytes", "maximum": maxBytes, "observed": size}}
	}
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
	return ReadContentCappedFromRoot(tool, access, nil, loc, displayPath, compressed, maxBytes)
}

// ReadContentCappedFromRoot loads one bounded regular file using an already
// held ReadRoot when provided, or opening loc.Root when root is nil.
func ReadContentCappedFromRoot(tool string, access Access, root *fseffect.ReadRoot, loc fseffect.Location, displayPath string, compressed bool, maxBytes int64) ([]byte, error) {
	var (
		f   *os.File
		err error
	)
	if root != nil {
		f, err = root.Open(loc.Rel)
	} else {
		f, err = fseffect.OpenRead(loc)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, DirectoryReject(displayPath)
	}
	if compressed {
		return readCompressedContentCapped(tool, access, f, displayPath, maxBytes)
	}
	size := info.Size()
	if size > maxBytes {
		return nil, access.SizeReject(tool, displayPath, size, maxBytes)
	}
	if size >= 0 && size <= 64<<10 {
		buf := make([]byte, size+1)
		n, readErr := io.ReadFull(f, buf)
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return buf[:n], nil
		}
		if readErr != nil {
			return nil, readErr
		}
		rest, rerr := io.ReadAll(io.LimitReader(f, maxBytes+1-int64(n)))
		buf = append(buf[:n], rest...)
		if int64(len(buf)) > maxBytes {
			return nil, access.SizeReject(tool, displayPath, int64(len(buf)), maxBytes)
		}
		if rerr != nil {
			return nil, rerr
		}
		return buf, nil
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
