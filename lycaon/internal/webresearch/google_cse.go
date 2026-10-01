package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
