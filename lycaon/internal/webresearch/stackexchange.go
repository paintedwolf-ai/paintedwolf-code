package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

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
