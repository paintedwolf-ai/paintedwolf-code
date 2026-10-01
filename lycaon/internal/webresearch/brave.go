package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
