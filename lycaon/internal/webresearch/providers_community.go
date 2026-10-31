package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
)

func discourseRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/search.json")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, _ Settings) error {
			req.Header.Set("Accept", "application/json")
			return nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseDiscourseHits(body, s, providerID, maxDiscourseHits)
		},
	}
}

const maxDiscourseHits = 25

func parseDiscourseHits(body []byte, s Settings, providerID string, max int) ([]WebHit, error) {
	var data struct {
		Posts []struct {
			TopicID int    `json:"topic_id"`
			Blurb   string `json:"blurb"`
		} `json:"posts"`
		Topics []struct {
			ID    int    `json:"id"`
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	topics := make(map[int]struct {
		Slug  string
		Title string
	}, len(data.Topics))
	for _, t := range data.Topics {
		topics[t.ID] = struct {
			Slug  string
			Title string
		}{Slug: t.Slug, Title: t.Title}
	}
	if max <= 0 {
		max = maxDiscourseHits
	}
	hits := make([]WebHit, 0, min(len(data.Posts), max))
	seen := make(map[int]struct{}, max)
	for _, p := range data.Posts {
		if p.TopicID == 0 {
			continue
		}
		if _, ok := seen[p.TopicID]; ok {
			continue
		}
		seen[p.TopicID] = struct{}{}
		topic := topics[p.TopicID]
		slug := strings.TrimSpace(topic.Slug)
		if slug == "" {
			slug = "topic"
		}
		title := webindex.NormalizeWebTextForStorage(topic.Title, 0)
		if title == "" {
			title = slug
		}
		hitURL := base + "/t/" + url.PathEscape(slug) + "/" + fmt.Sprintf("%d", p.TopicID)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  webindex.NormalizeWebTextForStorage(p.Blurb, 0),
			Provider: providerID,
		})
		if len(hits) >= max {
			break
		}
	}
	return hits, nil
}

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

const stackexchangeQuotaLowWater = 5

func stackexchangeRESTSpec(providerID, site string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/2.3/search/advanced")
			if err != nil {
				return "", err
			}
			count := max
			if count > 100 {
				count = 100
			}
			q := u.Query()
			q.Set("order", "desc")
			q.Set("sort", "relevance")
			q.Set("q", query)
			q.Set("site", site)
			q.Set("pagesize", fmt.Sprintf("%d", count))
			if key := strings.TrimSpace(s.Keys[providerID]); key != "" {
				q.Set("key", key)
			}
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			return parseStackExchangeHits(body, providerID)
		},
		GateRecordStatus: stackExchangeGateRecordStatus,
	}
}

func parseStackExchangeHits(body []byte, providerID string) ([]WebHit, error) {
	var data struct {
		Items []struct {
			Title      string `json:"title"`
			Link       string `json:"link"`
			Score      int    `json:"score"`
			IsAnswered bool   `json:"is_answered"`
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
		snippet := fmt.Sprintf("%d score · answered: %t", it.Score, it.IsAnswered)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.Link,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func stackExchangeGateRecordStatus(body []byte) (int, bool) {
	var data struct {
		QuotaRemaining int `json:"quota_remaining"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, false
	}
	if data.QuotaRemaining <= stackexchangeQuotaLowWater {
		return 403, true
	}
	return 0, false
}
