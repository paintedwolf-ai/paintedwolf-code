package guidance

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// StreamingCloseoutNarrative extracts synthesis prose from a partial envelope.
// ok is false when content is not a closeout envelope.
func StreamingCloseoutNarrative(content string) (string, bool) {
	if !CloseoutBodyIsEnvelopeShaped(content) {
		return "", false
	}
	if prose, ok := CoordinatorCloseoutTranscriptNarrative(content); ok {
		return prose, true
	}
	prose := partialJSONStringField(content, "synthesis")
	// Nested envelopes are invalid closeout synthesis.
	if CloseoutBodyIsEnvelopeShaped(prose) {
		return "", true
	}
	return prose, true
}

// partialJSONStringField decodes the available prefix of an envelope string.
func partialJSONStringField(doc, field string) string {
	body, ok := rawJSONStringBody(doc, field)
	if !ok {
		return ""
	}
	body = trimIncompleteRune(body)
	// An incomplete JSON escape occupies at most five trailing bytes.
	for trim := 0; trim <= 5 && trim <= len(body); trim++ {
		var out string
		if err := json.Unmarshal([]byte(`"`+body[:len(body)-trim]+`"`), &out); err == nil {
			return out
		}
	}
	return ""
}

// trimIncompleteRune omits partial UTF-8 characters until their bytes arrive.
func trimIncompleteRune(body string) string {
	for i := len(body) - 1; i >= 0 && i > len(body)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(body[i]) {
			continue
		}
		if r, size := utf8.DecodeRuneInString(body[i:]); r == utf8.RuneError && size == 1 {
			return body[:i]
		}
		return body
	}
	return body
}

// rawJSONStringBody escapes control bytes and preserves incomplete string content.
func rawJSONStringBody(doc, field string) (string, bool) {
	rest, ok := afterJSONStringOpen(doc, field)
	if !ok {
		return "", false
	}
	var body strings.Builder
	escaped := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case escaped:
			escaped = false
			body.WriteByte(c)
		case c == '\\':
			escaped = true
			body.WriteByte(c)
		case c == '"':
			return body.String(), true
		case c < 0x20:
			fmt.Fprintf(&body, `\u%04x`, c)
		default:
			body.WriteByte(c)
		}
	}
	return body.String(), true
}

// afterJSONStringOpen finds the start of the named string value.
func afterJSONStringOpen(doc, field string) (string, bool) {
	at := strings.Index(doc, `"`+field+`"`)
	if at < 0 {
		return "", false
	}
	rest := strings.TrimLeft(doc[at+len(field)+2:], " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return "", false
	}
	return rest[1:], true
}
