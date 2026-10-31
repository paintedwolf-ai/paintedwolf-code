package tools

import (
	"bytes"
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// RememberCommandSourceDelivery credits only complete source lines whose exact
// saved bytes were delivered. Transformed, screened or omitted bytes grant no read.
func RememberCommandSourceDelivery(ctx context.Context, tc ToolContext, memory *toolcontract.ProgramMemory, mapping toolcontract.StreamSourceMapping, delivered []byte) {
	documents, ok := ProjectDocuments(tc)
	if !ok {
		return
	}
	identity := strings.TrimPrefix(mapping.Source.SourceID, "disk:")
	root, path, ok := strings.Cut(identity, "/")
	if !ok || identity == mapping.Source.SourceID || root == "" || path == "" {
		return
	}
	switch mapping.Source.Representation {
	case toolcontract.OriginalEncodedBytes, toolcontract.UTF8SourceBytes, toolcontract.LogicalUTF8LF:
	default:
		return
	}
	if memory == nil || mapping.Source.EndByte <= 0 || mapping.Source.EndByte > sourceblob.MaxRevisionContentBytes {
		return
	}
	// Opening reconciles a bounded editable snapshot, which may have grown since
	// capture. Admit its maximum text, database and encoding overlap before that
	// owner allocates; optional read credit never stops already delivered output.
	release, err := memory.ReserveProgramState(ctx, 12*sourceblob.MaxRevisionContentBytes)
	if err != nil {
		return
	}
	defer release()
	branch, err := tc.SourceBranch(root)
	if err != nil {
		return
	}
	doc, open, err := documents.OpenDocument(ctx, tc.Identity.ProjectID, branch, root, path)
	if err != nil || !open {
		return
	}
	if doc.Dirty || doc.Diverged || doc.Absent || mapping.Source.Revision != "sha256:"+doc.BaseSHA256 {
		return
	}
	content := doc.Text
	if doc.EOL == "crlf" {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	if mapping.Source.Representation == toolcontract.OriginalEncodedBytes && doc.Encoding != textfile.UTF8 && doc.Encoding != textfile.UTF8BOM {
		return
	}
	raw, err := textfile.EncodeBounded(content, doc.Encoding, textfile.LimitsForRaw(max(int64(len(content))*4+4, 1)))
	if err != nil {
		return
	}
	if mapping.Source.Representation == toolcontract.LogicalUTF8LF {
		raw = []byte(doc.Text)
	}
	if mapping.Source.Representation == toolcontract.UTF8SourceBytes {
		// Text captures remove a BOM while preserving the source's line endings.
		raw = []byte(content)
	}
	if textfile.SHA256(raw) != mapping.Source.SHA256 {
		return
	}
	start, end := mapping.SourceStartByte, mapping.SourceEndByte
	if start < 0 || end <= start || end > int64(len(raw)) || !bytes.Equal(raw[start:end], delivered) {
		return
	}
	// Discard partial boundary lines; seeing a fragment never authorizes replacing
	// the rest of that line.
	first := start
	if first > 0 && raw[first-1] != '\n' {
		n := bytes.IndexByte(raw[first:end], '\n')
		if n < 0 {
			return
		}
		first += int64(n + 1)
	}
	last := end
	if last < int64(len(raw)) && raw[last-1] != '\n' && raw[last] != '\n' && !(raw[last] == '\r' && last+1 < int64(len(raw)) && raw[last+1] == '\n' && doc.EOL == "crlf") {
		n := bytes.LastIndexByte(raw[first:last], '\n')
		if n < 0 {
			return
		}
		last = first + int64(n+1)
	}
	if last <= first {
		return
	}
	if first == 0 && last == int64(len(raw)) {
		documents.RememberAgentRead(tc.Identity.ProjectID, tc.Identity.SessionID, doc)
		return
	}
	line := 1 + bytes.Count(raw[:first], []byte{'\n'})
	endLine := line + bytes.Count(bytes.TrimSuffix(raw[first:last], []byte{'\n'}), []byte{'\n'})
	documents.RememberAgentReadSpans(tc.Identity.ProjectID, tc.Identity.SessionID, doc, []documentcore.LineSpan{{StartLine: line, EndLine: endLine}})
}
