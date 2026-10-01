package evidence

import "testing"

// The record carries each page title the retrieval tool observed, so a citation
// of that URL can name the page.
func TestBuildEvidenceRecord_CarriesObservedPageTitles(t *testing.T) {
	search := BuildEvidenceRecord("", "web_search", map[string]any{"query": "pytorch pickle"},
		`{"ok":true,"query":"pytorch pickle","results":[`+
			`{"title":"Serialization semantics","url":"https://docs.pytorch.org/notes/serialization.html"},`+
			`{"title":"","url":"https://example.test/untitled"}]}`)

	titles := search.URLTitles()
	if got := titles["https://docs.pytorch.org/notes/serialization.html"]; got != "Serialization semantics" {
		t.Fatalf("search title = %q, want the result's own title", got)
	}
	if _, ok := titles["https://example.test/untitled"]; ok {
		t.Fatal("a result with no title must record none rather than an empty one")
	}
}

// A fetch records its title against both the URL asked for and the one the
// tool reports, which differ after a redirect.
func TestBuildEvidenceRecord_FetchTitleCoversRedirects(t *testing.T) {
	rec := BuildEvidenceRecord("", "fetch_url", map[string]any{"url": "https://ffmpeg.org/security"},
		`{"url":"https://ffmpeg.org/security.html","status":200,"title":"FFmpeg security","text":"…"}`)

	titles := rec.URLTitles()
	for _, u := range []string{"https://ffmpeg.org/security", "https://ffmpeg.org/security.html"} {
		if titles[u] != "FFmpeg security" {
			t.Fatalf("title for %s = %q, want the observed page title", u, titles[u])
		}
	}
}

// A search record reports how many lines matched and in how many files; the
// appendix states these in place of a path and line.
func TestGrepMatchStats_CountsLinesAndFiles(t *testing.T) {
	rec := BuildEvidenceRecord(t.TempDir(), "grep", map[string]any{"pattern": "torch.load"},
		`{"matches":[`+
			`{"path":"a.py","line":3,"content":"torch.load(p)"},`+
			`{"path":"a.py","line":9,"content":"torch.load(q)"},`+
			`{"path":"b.py","line":1,"content":"torch.load(r)"}]}`)

	lines, paths := GrepMatchStats(rec)
	if lines != 3 || paths != 2 {
		t.Fatalf("match stats = %d lines in %d files, want 3 in 2", lines, paths)
	}
	if lines, paths := GrepMatchStats(Record{}); lines != 0 || paths != 0 {
		t.Fatalf("empty record stats = %d/%d, want zero", lines, paths)
	}
}

// The durable round-trip carries the titles, so a report assembled after a
// restart still names its sources.
func TestRecordAux_RoundTripsURLTitles(t *testing.T) {
	rec := Record{Kind: "web", Handle: "web#1"}
	rec.touchURL("https://example.test/a")
	rec.touchURLTitle("https://example.test/a", "Example page")

	aux := CloneRecordForStore(rec)
	var restored Record
	ApplyRecordAux(&restored, aux)
	if got := restored.URLTitles()["https://example.test/a"]; got != "Example page" {
		t.Fatalf("restored title = %q, want it to survive the store round-trip", got)
	}
}
