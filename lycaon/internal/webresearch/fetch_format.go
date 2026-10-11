package webresearch

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"net/url"
	"path"
	"strings"
)

const (
	// inlineFullMaxBytes is the body size at or below which the whole page is
	// returned inline. It sits under the compaction chunk threshold so small
	// fetches never spill and never get compacted.
	inlineFullMaxBytes = 8000
	// headLines is how many leading lines are shown verbatim alongside the map.
	headLines = 60
	// maxMapEntries caps the symbol map so orientation stays compact.
	maxMapEntries = 50
	// fetchThinTextMaxBytes marks an HTML page whose extracted text is so
	// short it is almost certainly a JS-rendered shell (footer-only pages run
	// ~250 bytes). The note stops agents from re-fetching to find more.
	fetchThinTextMaxBytes = 300
)

// fetchThinNote is appended when an HTML page yields effectively no text.
const fetchThinNote = "note: page yielded almost no readable text — likely rendered client-side by JavaScript; re-fetching will not return more."

// FormatFetchResult renders the orientation view of a fetched page: small bodies
// inline, large bodies as a head plus a structural outline. The full body is
// cached for fetch_url offset/limit ranges.
func FormatFetchResult(res FetchURLResult, outline fileoutline.Result, cacheHit bool) string {
	var b strings.Builder
	if res.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", res.Title)
	}

	if len(res.Text) <= inlineFullMaxBytes {
		// Small body: fully inline; nothing to page.
		b.WriteString(res.Text)
		if res.Markdown && res.Status >= 200 && res.Status < 300 &&
			len(strings.TrimSpace(res.Text)) < fetchThinTextMaxBytes {
			fmt.Fprintf(&b, "\n\n%s\n", fetchThinNote)
		}
		writeFooter(&b, res, 0)
		return b.String()
	}

	lines := strings.Split(res.Text, "\n")
	total := len(lines)
	verb := "fetched"
	if cacheHit {
		verb = "cache hit (not re-fetched)"
	}
	fmt.Fprintf(&b, "[%s] %s\n", verb, res.URL)
	writeFooter(&b, res, total)
	b.WriteString("\nFull body cached. Read any range with fetch_url(url, offset, limit) — 1-based lines, no re-download.\n")
	writeOutlineMap(&b, outline)

	head := lines
	if len(head) > headLines {
		head = head[:headLines]
	}
	fmt.Fprintf(&b, "\nhead (L1–%d of %d):\n", len(head), total)
	b.WriteString(strings.Join(head, "\n"))
	b.WriteString("\n")
	return b.String()
}

// writeOutlineMap renders the structural map: a log digest line for log-shaped
// bodies, otherwise the symbol outline (kind + name + line) so the model knows
// what ranges to page.
func writeOutlineMap(b *strings.Builder, outline fileoutline.Result) {
	if outline.LogDigest != nil {
		fmt.Fprintf(b, "\nlog digest (format %s, %d/%d records parsed) — page clusters with fetch_url offset/limit\n",
			outline.LogDigest.Format, outline.LogDigest.ParsedCount, outline.LogDigest.RecordCount)
		return
	}
	if len(outline.Symbols) == 0 {
		return
	}
	syms := outline.Symbols
	truncated := false
	if len(syms) > maxMapEntries {
		syms = syms[:maxMapEntries]
		truncated = true
	}
	fmt.Fprintf(b, "\nmap (%s outline — fetch_url with offset/limit to read any range):\n", outline.Source)
	for _, s := range syms {
		fmt.Fprintf(b, "  L%-5d %s %s\n", s.Line, s.Kind, s.Name)
	}
	if truncated {
		b.WriteString("  … more — page later ranges with fetch_url offset/limit\n")
	}
}

// FormatFetchRange renders a verbatim line range [offset, offset+limit) of a
// cached body, with 1-based line numbers so excerpts cite real lines.
func FormatFetchRange(url, body string, offset, limit int) string {
	lines := strings.Split(body, "\n")
	total := len(lines)
	if offset < 1 {
		offset = 1
	}
	start := offset - 1
	if start >= total {
		var b strings.Builder
		fmt.Fprintf(&b, "[%s] L%d is past end of body (%d lines).\n", url, offset, total)
		return b.String()
	}
	end := start + limit
	if limit <= 0 || end > total {
		end = total
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] lines %d–%d of %d\n", url, start+1, end, total)
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d|%s\n", i+1, lines[i])
	}
	return b.String()
}

func writeFooter(b *strings.Builder, res FetchURLResult, totalLines int) {
	b.WriteString("\n---\n")
	fmt.Fprintf(b, "status: %d", res.Status)
	if res.ContentType != "" {
		fmt.Fprintf(b, " | %s", res.ContentType)
	}
	if totalLines > 0 {
		fmt.Fprintf(b, " | %d lines, %d bytes", totalLines, len(res.Text))
	}
	b.WriteString("\n")
}

func fetchResultCacheEntry(res FetchURLResult, ext string) tooloutput.FetchCacheEntry {
	return tooloutput.FetchCacheEntry{
		URL:         res.URL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Title:       res.Title,
		Body:        res.Text,
		Ext:         ext,
		Markdown:    res.Markdown,
	}
}

func rawResultCacheEntry(res FetchRawResult, body string) tooloutput.FetchCacheEntry {
	return tooloutput.FetchCacheEntry{
		URL:         res.URL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Body:        body,
		Ext:         fetchRawCacheExt(res),
	}
}

func fetchResultFromCache(entry tooloutput.FetchCacheEntry) FetchURLResult {
	return FetchURLResult{
		URL:         entry.URL,
		Status:      entry.Status,
		ContentType: entry.ContentType,
		Title:       entry.Title,
		Text:        entry.Body,
		Markdown:    entry.Markdown,
	}
}

func fetchCacheExt(res FetchURLResult) string {
	if res.Markdown {
		return "md"
	}
	// The declared media type selects the outline grammar for extensionless URLs.
	if jsonMediaType(res.ContentType) {
		return "json"
	}
	if u, err := url.Parse(res.URL); err == nil {
		if e := path.Ext(u.Path); len(e) > 1 {
			return e[1:]
		}
	}
	return "txt"
}

func fetchRawCacheExt(res FetchRawResult) string {
	if u, err := url.Parse(res.URL); err == nil {
		if e := path.Ext(u.Path); len(e) > 1 {
			return e[1:]
		}
	}
	ct := mediaTypeOnly(res.ContentType)
	switch {
	case strings.Contains(ct, "javascript"):
		return "js"
	case strings.Contains(ct, "json"):
		return "json"
	case strings.Contains(ct, "css"):
		return "css"
	case strings.Contains(ct, "svg"):
		return "svg"
	case strings.Contains(ct, "html"):
		return "html"
	case strings.Contains(ct, "xml"):
		return "xml"
	}
	return "txt"
}
