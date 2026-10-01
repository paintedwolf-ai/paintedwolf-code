package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func openLibraryRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "openlibrary",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "openlibrary")
			if base == "" {
				return "", fmt.Errorf("openlibrary endpoint not configured")
			}
			u, err := url.Parse(base + "/search.json")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("limit", fmt.Sprintf("%d", min(max, 25)))
			q.Set("fields", "key,title,author_name,first_publish_year")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: parseOpenLibraryHits,
	}
}

func parseOpenLibraryHits(body []byte, s Settings) ([]WebHit, error) {
	var data struct {
		Docs []struct {
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			AuthorName       []string `json:"author_name"`
			FirstPublishYear int      `json:"first_publish_year"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, "openlibrary"), "/")
	if base == "" {
		base = "https://openlibrary.org"
	}
	hits := make([]WebHit, 0, len(data.Docs))
	for _, it := range data.Docs {
		key := strings.Trim(strings.TrimSpace(it.Key), "/")
		title := strings.TrimSpace(it.Title)
		if key == "" || title == "" {
			continue
		}
		parts := make([]string, 0, 2)
		if len(it.AuthorName) > 0 {
			parts = append(parts, strings.Join(it.AuthorName, ", "))
		}
		if it.FirstPublishYear > 0 {
			parts = append(parts, fmt.Sprintf("%d", it.FirstPublishYear))
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      base + "/" + key,
			Snippet:  strings.Join(parts, " · "),
			Provider: "openlibrary",
		})
	}
	return hits, nil
}
