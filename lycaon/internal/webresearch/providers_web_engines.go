package webresearch

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
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

const braveSearchURL = "https://api.search.brave.com/res/v1/web/search"

func braveRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "brave",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, maxResults int) (string, error) {
			u, err := url.Parse(braveSearchURL)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			count := maxResults
			if count > 20 {
				count = 20
			}
			q.Set("count", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("X-Subscription-Token", s.Keys["brave"])
			return nil
		},
		ParseHits: parseBraveHits,
	}
}

func parseBraveHits(body []byte) ([]WebHit, error) {
	var data struct {
		Web struct {
			Results []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Web.Results))
	for _, it := range data.Web.Results {
		if it.URL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = it.URL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.URL,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Description, 0),
			Provider: "brave",
		})
	}
	return hits, nil
}

func gdeltRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "gdelt",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "gdelt")
			if base == "" {
				return "", fmt.Errorf("gdelt endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v2/doc/doc")
			if err != nil {
				return "", err
			}
			count := max
			if count > 250 {
				count = 250
			}
			q := u.Query()
			q.Set("query", query)
			q.Set("mode", "ArtList")
			q.Set("maxrecords", fmt.Sprintf("%d", count))
			q.Set("format", "json")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits:  parseGDELTHits,
		TimeoutSec: 25,
	}
}

func parseGDELTHits(body []byte) ([]WebHit, error) {
	var data struct {
		Articles []struct {
			URL      string `json:"url"`
			Title    string `json:"title"`
			Domain   string `json:"domain"`
			Language string `json:"language"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Articles))
	for _, it := range data.Articles {
		hitURL := strings.TrimSpace(it.URL)
		if hitURL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = hitURL
		}
		snippet := strings.TrimSpace(it.Domain)
		if lang := strings.TrimSpace(it.Language); lang != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += lang
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: "gdelt",
		})
	}
	return hits, nil
}

const googleCSESearchURL = "https://www.googleapis.com/customsearch/v1"

func googleCseRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "google_cse",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			u, err := url.Parse(googleCSESearchURL)
			if err != nil {
				return "", err
			}
			count := max
			if count > 10 {
				count = 10
			}
			q := u.Query()
			q.Set("cx", s.Config["google_cse"]["search_engine_id"])
			q.Set("q", query)
			q.Set("num", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			if key := strings.TrimSpace(s.Keys["google_cse"]); key != "" {
				req.Header.Set("X-goog-api-key", key)
			}
			return nil
		},
		ParseHits: parseGoogleCSEHits,
	}
}

func parseGoogleCSEHits(body []byte) ([]WebHit, error) {
	var data struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Items))
	for _, it := range data.Items {
		if it.Link == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = it.Link
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.Link,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Snippet, 0),
			Provider: "google_cse",
		})
	}
	return hits, nil
}
