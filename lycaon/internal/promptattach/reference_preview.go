package promptattach

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Reference previews bound both bytes scanned and bytes retained; a path stays readable in place.
func externalReferencePreview(r io.Reader, name, mime, hint string, size int64, start, end int, budget *TurnPreviewBudget) string {
	allowance := budget.claimText(size + int64(len(hint)+1))
	if start > 0 {
		return selectedReferencePreview(r, name, mime, hint, start, end, allowance)
	}
	contentLimit := allowance - len(hint) - 1
	if contentLimit <= 0 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(contentLimit+utf8.UTFMax)))
	if err != nil {
		return ""
	}
	preview, _, clipped := truncatePrefix(string(raw), contentLimit)
	if !utf8.ValidString(preview) || strings.ContainsRune(preview, 0) {
		return ""
	}
	truncated := clipped || size > int64(len(raw))
	body := hint + "\n" + preview
	if truncated {
		body += "\n" + TruncationMarker(int64(len(preview)), size)
	}
	return FormatFence(name, mime, body, truncated, fmt.Sprintf("bytes=%q", fmt.Sprint(size)))
}

func selectedReferencePreview(r io.Reader, name, mime, hint string, start, end, allowance int) string {
	limit := allowance - len(hint) - 1
	if limit <= 0 {
		return ""
	}
	scanner := bufio.NewScanner(io.LimitReader(r, Active().Materialization.MaxBody.Int64()))
	scanner.Buffer(make([]byte, min(limit+1, 4096)), limit+1)
	var text strings.Builder
	for line := 1; line <= end && scanner.Scan(); line++ {
		if line < start {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(scanner.Text())
		if text.Len() >= limit {
			break
		}
	}
	if scanner.Err() != nil || text.Len() == 0 || !utf8.ValidString(text.String()) || strings.ContainsRune(text.String(), 0) {
		return ""
	}
	return frameSnippet(name, mime, hint+"\n"+text.String(), allowance)
}
