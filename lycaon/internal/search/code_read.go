package search

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
)

const (
	codeExecutorMaxFileBytes = 1 << 20
	codeReadReplaceAttempts  = 3
)

type codeReadHandle interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

func openCodeReadFile(abs string) (codeReadHandle, error) {
	return fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(abs), Rel: filepath.Base(abs)})
}

// codeOpenStatus classifies why a listed file yielded no searchable text.
type codeOpenStatus int

const (
	codeOpenOK codeOpenStatus = iota
	// codeOpenMissing marks a file removed or replaced by an ineligible leaf since listing.
	codeOpenMissing
	codeOpenBinary
	// codeOpenUnsearchable contributes to the skipped-file count.
	codeOpenUnsearchable
)

// readCodeBytes reads one regular file up to maxBytes, retrying boundedly when
// an atomic regular-file replacement swaps the inode between lookup and open.
func readCodeBytes(abs string, maxBytes int64) ([]byte, codeOpenStatus) {
	return readCodeBytesWith(abs, maxBytes, os.Lstat, openCodeReadFile)
}

func readCodeBytesWith(
	abs string,
	maxBytes int64,
	lstat func(string) (os.FileInfo, error),
	openRead func(string) (codeReadHandle, error),
) ([]byte, codeOpenStatus) {
	if maxBytes <= 0 {
		maxBytes = codeExecutorMaxFileBytes
	}
	for attempt := 0; attempt < codeReadReplaceAttempts; attempt++ {
		pathInfo, err := lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, codeOpenMissing
			}
			return nil, codeOpenUnsearchable
		}
		if !pathInfo.Mode().IsRegular() {
			return nil, codeOpenMissing
		}
		f, err := openRead(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, codeOpenMissing
			}
			return nil, codeOpenUnsearchable
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, codeOpenUnsearchable
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			return nil, codeOpenMissing
		}
		if !os.SameFile(pathInfo, info) {
			_ = f.Close()
			continue
		}
		if info.Size() > maxBytes {
			_ = f.Close()
			return nil, codeOpenUnsearchable
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		_ = f.Close()
		if err != nil || int64(len(raw)) > maxBytes {
			return nil, codeOpenUnsearchable
		}
		return raw, codeOpenOK
	}
	return nil, codeOpenUnsearchable
}

// codeContent is one file's searchable UTF-8 payload.
type codeContent struct {
	// UTF-8 reuses the read buffer; UTF-16 is transcoded.
	bytes []byte
}

func (c codeContent) text() string { return string(c.bytes) }

// readCodeContent preserves the UTF-8 buffer for literal prefiltering.
func readCodeContent(abs string, maxBytes int64) (codeContent, codeOpenStatus) {
	raw, status := readCodeBytes(abs, maxBytes)
	if status != codeOpenOK {
		return codeContent{}, status
	}
	detection := textfile.Classify(raw)
	switch detection.Encoding {
	case textfile.UTF8:
		return codeContent{bytes: raw}, codeOpenOK
	case textfile.UTF8BOM:
		return codeContent{bytes: bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))}, codeOpenOK
	}
	if detection.Class != textfile.Supported {
		return codeContent{}, codeOpenBinary
	}
	text, _, err := textfile.Decode(raw, textfile.LimitsForRaw(maxBytes))
	if err != nil {
		return codeContent{}, codeOpenBinary
	}
	return codeContent{bytes: []byte(text)}, codeOpenOK
}

// openCodeDocument retains encoding and raw digest for replacement planning.
func openCodeDocument(abs string, maxBytes int64) (textfile.UntrustedDocument, codeOpenStatus) {
	raw, status := readCodeBytes(abs, maxBytes)
	if status != codeOpenOK {
		return textfile.UntrustedDocument{}, status
	}
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(maxBytes))
	if err != nil {
		return textfile.UntrustedDocument{}, codeOpenBinary
	}
	return doc, codeOpenOK
}
