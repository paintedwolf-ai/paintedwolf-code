package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
)

const kagiSearchURL = "https://kagi.com/api/v1/search"

func kagiRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "kagi",
		Method:      http.MethodPost,
		ContentType: "application/json",
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return kagiSearchURL, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			count := max
			if count > 20 {
				count = 20
			}
			return surveyjson.Marshal(map[string]any{
				"query":    query,
				"workflow": "search",
				"format":   "json",
				"limit":    count,
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("Authorization", "Bearer "+s.Keys["kagi"])
			return nil
		},
		ParseHits: parseKagiHits,
	}
}

func parseKagiHits(body []byte) ([]WebHit, error) {
	var data struct {
		Data struct {
			Search []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Data.Search))
	for _, it := range data.Data.Search {
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
			Snippet:  webindex.NormalizeWebTextForStorage(it.Snippet, 0),
			Provider: "kagi",
		})
	}
	return hits, nil
}

const marginaliaSearchURL = "https://api2.marginalia-search.com/search"

func marginaliaRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "marginalia",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, maxResults int) (string, error) {
			u, err := url.Parse(marginaliaSearchURL)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("query", query)
			count := maxResults
			if count > 30 {
				count = 30
			}
			q.Set("count", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("API-Key", s.Keys["marginalia"])
			return nil
		},
		ParseHits: parseMarginaliaHits,
	}
}

func parseMarginaliaHits(body []byte) ([]WebHit, error) {
	var response struct {
		Results []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	rows := response.Results
	hits := make([]WebHit, 0, len(rows))
	for _, it := range rows {
		if it.URL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = webindex.NormalizeWebTextForStorage(it.Description, 0)
		}
		if title == "" {
			title = it.URL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.URL,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Description, 0),
			Provider: "marginalia",
		})
	}
	return hits, nil
}

const mwmblDefaultSearchPath = "/api/v1/search/"

func mwmblRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "mwmbl",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			// Default endpoint comes from the catalog via resolveProviderConfig.
			base := strings.TrimSuffix(strings.TrimSpace(s.Config["mwmbl"]["endpoint"]), "/")
			if base == "" {
				return "", fmt.Errorf("mwmbl endpoint not configured")
			}
			u, err := url.Parse(base + mwmblDefaultSearchPath)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("s", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMwmblHits,
	}
}

type mwmblTextPart struct {
	Value string `json:"value"`
}

type mwmblResultRow struct {
	URL     string          `json:"url"`
	Title   []mwmblTextPart `json:"title"`
	Extract []mwmblTextPart `json:"extract"`
}

func mwmblJoinText(parts []mwmblTextPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Value)
	}
	return strings.TrimSpace(b.String())
}

func parseMwmblHits(body []byte) ([]WebHit, error) {
	var rows []mwmblResultRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parse mwmbl response: %w", err)
	}
	hits := make([]WebHit, 0, len(rows))
	for _, it := range rows {
		if it.URL == "" {
			continue
		}
		snippet := mwmblJoinText(it.Extract)
		title := webindex.NormalizeWebTextForStorage(mwmblJoinText(it.Title), 0)
		if title == "" {
			title = it.URL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.URL,
			Snippet:  webindex.NormalizeWebTextForStorage(snippet, 0),
			Provider: "mwmbl",
		})
	}
	return hits, nil
}

func searxngRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "searxng",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := strings.TrimSuffix(strings.TrimSpace(s.Config["searxng"]["endpoint"]), "/")
			if base == "" {
				return "", fmt.Errorf("searxng endpoint not configured")
			}
			u, err := url.Parse(base + "/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("format", "json")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			if key := strings.TrimSpace(s.Keys["searxng"]); key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			return nil
		},
		ParseHits: parseSearxngHits,
	}
}

func parseSearxngHits(body []byte) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
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
			Snippet:  webindex.NormalizeWebTextForStorage(it.Content, 0),
			Provider: "searxng",
		})
	}
	return hits, nil
}
