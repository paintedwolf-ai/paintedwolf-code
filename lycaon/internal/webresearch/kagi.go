package webresearch

import (
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/webindex"
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
