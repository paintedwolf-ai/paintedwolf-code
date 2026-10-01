package webresearch

import (
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/internal/webindex"
)

const serperSearchURL = "https://google.serper.dev/search"

func serperRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "serper",
		Method:      http.MethodPost,
		ContentType: "application/json",
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return serperSearchURL, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			count := max
			if count > 100 {
				count = 100
			}
			return json.Marshal(map[string]any{
				"q":   query,
				"num": count,
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("X-API-KEY", s.Keys["serper"])
			return nil
		},
		ParseHits: parseSerperHits,
	}
}

func parseSerperHits(body []byte) ([]WebHit, error) {
	var data struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Organic))
	for _, it := range data.Organic {
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
			Provider: "serper",
		})
	}
	return hits, nil
}
