package sourceview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// OpenDocument returns the accepted shared document for a project-tree path.
// A failed lookup is an error, never a silent fall back to disk.
func OpenDocument(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (tools.EditorDocumentText, bool, error) {
	documents, ok := DocumentsFor(tctx, resolved)
	if !ok {
		return tools.EditorDocumentText{}, false, nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return tools.EditorDocumentText{}, false, err
	}
	doc, open, err := documents.OpenDocument(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel)
	if errors.Is(err, projectsource.ErrSourceIsDirectory) {
		return tools.EditorDocumentText{}, false, DirectoryReject(resolved.DisplayPath)
	}
	if err != nil {
		return tools.EditorDocumentText{}, false, fmt.Errorf("read collaborative document: %w", err)
	}
	return doc, open, nil
}

// ErrDocumentUnrepresentable reports accepted text its recorded encoding cannot hold.
var ErrDocumentUnrepresentable = errors.New("accepted document is not representable in its recorded encoding")

// DocumentBytes encodes accepted text exactly as a save writes it: the
// recorded encoding, BOM and newline policy. A clean document whose published
// bytes do not round-trip (mixed newlines) is served from those bytes, which
// are its accepted state. The result never exceeds maxBytes.
func DocumentBytes(doc tools.EditorDocumentText, resolved projectpaths.Resolved, maxBytes int64) ([]byte, error) {
	encoding := doc.Encoding
	if encoding == "" {
		encoding = textfile.UTF8
	}
	text := doc.Text
	if doc.EOL == "crlf" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	encoded, err := textfile.EncodeBounded(text, encoding, textfile.LimitsForRaw(maxBytes))
	if textTooLarge(err) {
		return nil, AccessRead.SizeReject("read", resolved.DisplayPath, int64(len(text)), maxBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDocumentUnrepresentable, err)
	}
	if textfile.SHA256(encoded) == doc.SHA256 {
		return encoded, nil
	}
	if doc.Dirty {
		return nil, ErrDocumentUnrepresentable
	}
	raw, err := ReadContentCapped("read", AccessRead, resolved.EffectLocation(), resolved.DisplayPath, resolved.Compressed, maxBytes)
	if err != nil {
		return nil, err
	}
	if textfile.SHA256(raw) != doc.SHA256 {
		return nil, ErrDocumentUnrepresentable
	}
	return raw, nil
}
