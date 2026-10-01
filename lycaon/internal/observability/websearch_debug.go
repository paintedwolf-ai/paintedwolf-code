package observability

import (
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// WebSearchDebugEnabled reports whether per-fetch web-search rows are mirrored
// to a debug JSONL file.
func WebSearchDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindWebSearch)
}

// WebSearchFetchCapture is one crawl or probe fetch inside a web search or
// index warm, written when LYCAON_WEBSEARCH_DEBUG is enabled. The slog summary
// lines answer "how long did this search take and where"; these rows answer
// "which fetch ate it".
type WebSearchFetchCapture struct {
	SearchID string
	// Phase is the pipeline phase at fetch time: memory, seed, crawl, frontier.
	Phase string
	// Kind is the fetch flavor: probe (verification GET), index (sitemap/feed/
	// robots/homepage crawl fetch), robots (first-contact robots.txt).
	Kind    string
	URL     string
	Host    string
	Source  string
	Status  int
	FetchMs int64
	// Verdict for probes: hit, thin, irrelevant, dead, skipped.
	Verdict string
}

type webSearchFetchEntry struct {
	Time     time.Time `json:"ts"`
	SearchID string    `json:"search_id,omitempty"`
	Phase    string    `json:"phase,omitempty"`
	Kind     string    `json:"kind"`
	URL      string    `json:"url"`
	Host     string    `json:"host,omitempty"`
	Source   string    `json:"source,omitempty"`
	Status   int       `json:"status,omitempty"`
	FetchMs  int64     `json:"fetch_ms"`
	Verdict  string    `json:"verdict,omitempty"`
}

var (
	webSearchDebugOnce sync.Once
	webSearchDebug     *jsonlDebugLog
)

func activeWebSearchDebugLog() *jsonlDebugLog {
	webSearchDebugOnce.Do(func() {
		if !WebSearchDebugEnabled() {
			return
		}
		log, err := openJSONLDebugLog(true, debugpaths.KindWebSearch)
		if err != nil {
			slog.Warn("web-search debug logging disabled", "err", err)
			return
		}
		webSearchDebug = log
		slog.Info("web-search debug logging enabled", "path", log.path)
	})
	return webSearchDebug
}

// LogWebSearchFetch records one fetch row with a redacted URL.
func LogWebSearchFetch(cap WebSearchFetchCapture) {
	log := activeWebSearchDebugLog()
	if log == nil {
		return
	}
	log.write(webSearchFetchEntry{
		Time:     time.Now().UTC(),
		SearchID: cap.SearchID,
		Phase:    cap.Phase,
		Kind:     cap.Kind,
		URL:      RedactURLForLog(cap.URL),
		Host:     cap.Host,
		Source:   cap.Source,
		Status:   cap.Status,
		FetchMs:  cap.FetchMs,
		Verdict:  cap.Verdict,
	})
}
