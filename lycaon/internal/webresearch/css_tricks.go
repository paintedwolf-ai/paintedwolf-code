package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func cssTricksRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "css_tricks",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "css_tricks")
			if base == "" {
				return "", fmt.Errorf("css_tricks endpoint not configured")
			}
			u, err := url.Parse(base + "/wp-json/wp/v2/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("per_page", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCSSTricksHits,
	}
}

func parseCSSTricksHits(body []byte) ([]WebHit, error) {
	var items []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(items))
	for _, item := range items {
		hitURL := strings.TrimSpace(item.URL)
		if hitURL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(item.Title, 0)
		if title == "" {
			title = hitURL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.TrimSpace(item.Subtype),
			Provider: "css_tricks",
		})
	}
	return hits, nil
}
