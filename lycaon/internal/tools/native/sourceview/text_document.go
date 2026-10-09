package sourceview

import (
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// ReadTextDocument retains encoding and raw identity for matching writes.
// Retained tool output keeps the session's spill bound for either access.
func ReadTextDocument(tool string, access Access, resolved projectpaths.Resolved, maxSpillBytes int) (textfile.UntrustedDocument, error) {
	maxBytes := access.MaxBytes()
	if resolved.ToolOutput {
		maxBytes = int64(tooloutput.EffectiveMaxSpillFileBytes(maxSpillBytes))
	}
	raw, err := ReadContentCapped(tool, access, resolved.EffectLocation(), resolved.DisplayPath, resolved.Compressed, maxBytes)
	if err != nil {
		return textfile.UntrustedDocument{}, err
	}
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(maxBytes))
	if err == nil {
		return doc, nil
	}
	if textTooLarge(err) {
		return textfile.UntrustedDocument{}, access.SizeReject(tool, resolved.DisplayPath, int64(len(raw)), maxBytes)
	}
	data := map[string]any{"path": resolved.DisplayPath, "bytes": len(raw), "reason": err.Error()}
	ext := strings.ToLower(filepath.Ext(resolved.DisplayPath))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".ico", ".bmp", ".tiff":
		data["is_image"] = true
	}
	return textfile.UntrustedDocument{}, &toolrejection.ToolReject{Code: "READ_BINARY_DENIED", Data: data}
}

// EncodeText encodes mutation content in the opened file's representation,
// or as UTF-8 for a create. Content above the mutation budget rejects with
// EDIT_FILE_TOO_LARGE; only the UTF-8 length is a measured size, since another
// encoding's length is unknown once its encoder stops.
func EncodeText(tool, path string, doc *textfile.UntrustedDocument, content string) ([]byte, error) {
	var encoded []byte
	var err error
	measured := int64(0)
	if doc != nil {
		encoded, err = doc.Encode(content)
	} else {
		encoded, err = textfile.EncodeBounded(content, textfile.UTF8, textfile.LimitsForRaw(readcaps.MaxMutationBytes))
		measured = int64(len(content))
	}
	if textTooLarge(err) {
		return nil, MutationSizeReject(tool, path, measured)
	}
	if err != nil {
		return nil, fmt.Errorf("encode bounded text file: %w", err)
	}
	return encoded, nil
}

func textTooLarge(err error) bool {
	return errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge)
}
