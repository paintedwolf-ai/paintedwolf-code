package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func hnRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "hn",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "hn")
			if base == "" {
				return "", fmt.Errorf("hn endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v1/search")
			if err != nil {
				return "", err
			}
			count := max
			if count > 20 {
				count = 20
			}
			q := u.Query()
			q.Set("query", query)
			q.Set("tags", "story")
			q.Set("hitsPerPage", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseHNHits,
	}
}

func parseHNHits(body []byte) ([]WebHit, error) {
	var data struct {
		Hits []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			ObjectID    string `json:"objectID"`
			Points      int    `json:"points"`
			NumComments int    `json:"num_comments"`
			StoryTitle  string `json:"story_title"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Hits))
	for _, it := range data.Hits {
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = webindex.NormalizeWebTextForStorage(it.StoryTitle, 0)
		}
		hitURL := strings.TrimSpace(it.URL)
		discussNote := ""
		if hitURL == "" && it.ObjectID != "" {
			hitURL = "https://news.ycombinator.com/item?id=" + it.ObjectID
		} else if hitURL != "" && it.ObjectID != "" {
			discussNote = " · discussion: https://news.ycombinator.com/item?id=" + it.ObjectID
		}
		if hitURL == "" {
			continue
		}
		if title == "" {
			title = hitURL
		}
		snippet := fmt.Sprintf("%d points · %d comments%s", it.Points, it.NumComments, discussNote)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: "hn",
		})
	}
	return hits, nil
}
