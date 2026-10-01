package search

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
)

const codeExecutorMaxFileBytes = 1 << 20

// codeOpenStatus classifies why a listed file yielded no searchable text.
type codeOpenStatus int

const (
	codeOpenOK codeOpenStatus = iota
	// codeOpenMissing marks a file removed or replaced since listing.
	codeOpenMissing
	codeOpenBinary
	// codeOpenUnsearchable contributes to the skipped-file count.
	codeOpenUnsearchable
)

// readCodeBytes reads one regular file up to maxBytes, rejecting a path that
// was swapped between lookup and open.
func readCodeBytes(abs string, maxBytes int64) ([]byte, codeOpenStatus) {
	if maxBytes <= 0 {
		maxBytes = codeExecutorMaxFileBytes
	}
	pathInfo, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, codeOpenMissing
		}
		return nil, codeOpenUnsearchable
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, codeOpenMissing
	}
	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(abs), Rel: filepath.Base(abs)})
	if err != nil {
		return nil, codeOpenUnsearchable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return nil, codeOpenMissing
	}
	if info.Size() > maxBytes {
		return nil, codeOpenUnsearchable
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return nil, codeOpenUnsearchable
	}
	return raw, codeOpenOK
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
