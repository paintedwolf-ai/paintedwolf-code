package webresearch

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Summary string `xml:"summary"`
}

// parseAtomHits extracts search hits from an Atom syndication feed body.
func parseAtomHits(body []byte, providerID string, snippetMax int) ([]WebHit, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("parse atom feed: empty body")
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse atom feed: %w", err)
	}
	if len(feed.Entries) == 0 {
		return nil, nil
	}
	hits := make([]WebHit, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		url := strings.TrimSpace(entry.ID)
		if url == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(entry.Title, 0)
		if title == "" {
			title = url
		}
		snippet := truncateText(webindex.NormalizeWebTextForStorage(entry.Summary, 0), snippetMax)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      url,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}
