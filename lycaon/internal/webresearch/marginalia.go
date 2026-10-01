package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
