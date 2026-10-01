package promptattach

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/format"
)

// TruncationMarker formats the locked trailing truncation line.
func TruncationMarker(kept, total int64) string {
	return fmt.Sprintf("[attachment truncated: kept %d of %d bytes]", kept, total)
}

// BodyHint names the materialized body outside its untrusted fence.
func BodyHint(mime, path string) string {
	if format.IsStructuredQueryMIME(mime) {
		return fmt.Sprintf("[full attachment content: jq(path=%q) to query, or read(path=%q) with offset/limit]", path, path)
	}
	return fmt.Sprintf("[full attachment content: read(path=%q) with offset/limit, or grep(path=%q) to search]", path, path)
}

// CountLines treats a trailing newline as a terminator.
func CountLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func truncatePrefix(text string, maxBytes int) (prefix string, kept int, truncated bool) {
	if maxBytes <= 0 {
		return "", 0, text != ""
	}
	if len(text) <= maxBytes {
		return text, len(text), false
	}
	kept = maxBytes
	for kept > 0 && !utf8.RuneStart(text[kept]) {
		kept--
	}
	return text[:kept], kept, true
}

// frameTextBlob reads only enough bytes for a UTF-8-safe preview.
func frameTextBlob(store blobstore.Store, blob blobstore.Blob, mime string, maxBytes int) (string, error) {
	f, err := store.Open(blob)
	if err != nil {
		return "", attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	defer func() { _ = f.Close() }()

	readLimit := int64(0)
	if maxBytes > 0 {
		readLimit = int64(maxBytes + utf8.UTFMax)
	}
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(f), readLimit))
	if err != nil {
		return "", attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	preview, _, clipped := truncatePrefix(string(raw), maxBytes)
	if !utf8.ValidString(preview) {
		return "", attacherr.Unsupported(fmt.Sprintf("attachment %q is not valid UTF-8 text", blob.Name))
	}
	truncated := clipped || blob.Size > int64(len(raw))
	attrs := []string{fmt.Sprintf("bytes=%q", strconv.FormatInt(blob.Size, 10))}
	// Truncated previews omit an unknown full line count.
	if !truncated {
		attrs = append(attrs, fmt.Sprintf("lines=%q", strconv.Itoa(CountLines(preview))))
	}
	return frameWithHint(blob, mime, preview, truncated, blob.Size, int64(len(preview)), attrs), nil
}

// frameExtract points document text back to its source package.
func frameExtract(blob blobstore.Blob, mime, text string, maxBytes int, extraAttrs ...string) string {
	preview, kept, truncated := truncatePrefix(text, maxBytes)
	attrs := append([]string{
		fmt.Sprintf("bytes=%q", strconv.Itoa(len(text))),
		fmt.Sprintf("lines=%q", strconv.Itoa(CountLines(text))),
	}, extraAttrs...)
	return frameWithHint(blob, mime, preview, truncated, int64(len(text)), int64(kept), attrs)
}

// frameSnippet frames text without a materialized body.
func frameSnippet(name, mime, body string, maxBytes int) string {
	preview, kept, truncated := truncatePrefix(body, maxBytes)
	if truncated {
		preview += "\n" + TruncationMarker(int64(kept), int64(len(body)))
	}
	return FormatFence(name, mime, preview, truncated,
		fmt.Sprintf("bytes=%q", strconv.Itoa(len(body))),
		fmt.Sprintf("lines=%q", strconv.Itoa(CountLines(body))),
	)
}

func frameWithHint(blob blobstore.Blob, mime, body string, truncated bool, total, kept int64, attrs []string) string {
	if truncated {
		body += "\n" + TruncationMarker(kept, total)
	}
	attrs = append(attrs, fmt.Sprintf("path=%q", blob.Rel))
	return FormatFence(blob.Name, mime, body, truncated, attrs...) + "\n" + BodyHint(mime, blob.Rel)
}

// FormatFence builds a labeled attachment fence.
func FormatFence(filename, mime, body string, truncated bool, extraAttrs ...string) string {
	fence := fenceTicks(body)
	var b strings.Builder
	b.WriteString(fence)
	b.WriteString("attachment filename=\"")
	b.WriteString(escapeAttr(filename))
	b.WriteString("\" mime=\"")
	b.WriteString(escapeAttr(mime))
	b.WriteString("\" truncated=\"")
	b.WriteString(boolAttr(truncated))
	b.WriteByte('"')
	for _, a := range extraAttrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(a)
	}
	b.WriteByte('\n')
	b.WriteString(body)
	b.WriteByte('\n')
	b.WriteString(fence)
	return b.String()
}

func boolAttr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\n`)
	return s
}

func fenceTicks(body string) string {
	longest := 2 // so result length is at least 3
	count := 0
	for i := 0; i < len(body); i++ {
		if body[i] == '`' {
			count++
			if count > longest {
				longest = count
			}
			continue
		}
		count = 0
	}
	return strings.Repeat("`", longest+1)
}
