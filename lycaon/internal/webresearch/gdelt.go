package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
