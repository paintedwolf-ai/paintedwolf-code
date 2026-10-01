package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func mankierRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "mankier",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "mankier")
			if base == "" {
				return "", fmt.Errorf("mankier endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v2/mans/")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("q", shortenProviderQuery(query))
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMankierHits,
	}
}

func parseMankierHits(body []byte) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Name        string `json:"name"`
			Section     string `json:"section"`
			Description string `json:"description"`
			URL         string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
		hitURL := strings.TrimSpace(it.URL)
		if hitURL == "" {
			continue
		}
		title := strings.TrimSpace(it.Name)
		if sec := strings.TrimSpace(it.Section); sec != "" {
			if title != "" {
				title = title + "(" + sec + ")"
			} else {
				title = sec
			}
		}
		if title == "" {
			title = hitURL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.TrimSpace(it.Description),
			Provider: "mankier",
		})
	}
	return hits, nil
}
