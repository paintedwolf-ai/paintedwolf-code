package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
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
