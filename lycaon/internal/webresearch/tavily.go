package webresearch

import (
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/internal/webindex"
)

const tavilySearchURL = "https://api.tavily.com/search"

func tavilyRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "tavily",
		Method:      http.MethodPost,
		ContentType: "application/json",
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return tavilySearchURL, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			count := max
			if count > 20 {
				count = 20
			}
			return json.Marshal(map[string]any{
				"query":        query,
				"max_results":  count,
				"search_depth": "basic",
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("Authorization", "Bearer "+s.Keys["tavily"])
			return nil
		},
		ParseHits: parseTavilyHits,
	}
}

func parseTavilyHits(body []byte) ([]WebHit, error) {
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
			Provider: "tavily",
		})
	}
	return hits, nil
}
