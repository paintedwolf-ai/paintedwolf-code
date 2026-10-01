package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
