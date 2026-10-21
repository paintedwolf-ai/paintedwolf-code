package commandsurface

import (
	"fmt"
	"strings"
)

// hereDocumentElision replaces one here-document body and its terminator.
const hereDocumentElision = " ..."

// DisplayText renders the dispatched statement's original source as its
// canonical display. Each of its own here-document bodies, with the terminator
// line, is elided after the delimiter word, so a large file write is not echoed
// back to the model. Spans come from the validated syntax tree; the source is
// never rescanned.
func (s Statement) DisplayText(source []byte) (string, error) {
	start, end := s.Span.StartByte, s.Span.EndByte
	var elided []int
	for _, r := range s.Redirects {
		if r.HereDocument != nil {
			elided = append(elided, r.Target.Span.EndByte)
		}
	}
	if len(elided) > 0 {
		// A here-document statement's span ends at its last body, which may
		// follow later statements on the header line; the header ends at the
		// statement's own command and redirect words.
		end = start
		if s.Command != nil {
			end = max(end, s.Command.syntaxLocation().Span.EndByte)
		}
		for _, r := range s.Redirects {
			stop := r.Span.EndByte
			if r.HereDocument != nil {
				stop = r.Target.Span.EndByte
			}
			end = max(end, stop)
		}
	}
	if start < 0 || start > end || end > len(source) {
		return "", fmt.Errorf("statement span %d..%d is outside the %d-byte source", start, end, len(source))
	}
	var out strings.Builder
	out.Grow(end - start + len(elided)*len(hereDocumentElision))
	at := start
	// Redirects are in lexical order, so delimiter ends ascend within the header.
	for _, cut := range elided {
		out.Write(source[at:cut])
		out.WriteString(hereDocumentElision)
		at = cut
	}
	out.Write(source[at:end])
	return out.String(), nil
}
