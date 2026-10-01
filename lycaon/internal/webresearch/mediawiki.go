package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func mediawikiRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/w/rest.php/v1/search/page")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseMediaWikiRESTHits(body, s, providerID)
		},
	}
}

func parseMediaWikiRESTHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var data struct {
		Pages []struct {
			Title   string `json:"title"`
			Key     string `json:"key"`
			Excerpt string `json:"excerpt"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	hits := make([]WebHit, 0, len(data.Pages))
	for _, it := range data.Pages {
		key := strings.TrimPrefix(strings.TrimSpace(it.Key), "/wiki/")
		if key == "" {
			key = strings.TrimSpace(it.Title)
		}
		if key == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = key
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      mediaWikiPageURL(base, key),
			Snippet:  webindex.NormalizeWebTextForStorage(it.Excerpt, 0),
			Provider: providerID,
		})
	}
	return hits, nil
}

// mediawikiActionSpec uses action=query&list=search.
func mediawikiActionSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/api.php")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("action", "query")
			q.Set("list", "search")
			q.Set("srsearch", query)
			q.Set("srlimit", fmt.Sprintf("%d", count))
			q.Set("format", "json")
			q.Set("utf8", "1")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseMediaWikiActionHits(body, s, providerID)
		},
	}
}

func parseMediaWikiActionHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var data struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	hits := make([]WebHit, 0, len(data.Query.Search))
	for _, it := range data.Query.Search {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      mediaWikiPageURL(base, title),
			Snippet:  webindex.NormalizeWebTextForStorage(it.Snippet, 0),
			Provider: providerID,
		})
	}
	return hits, nil
}

func mediaWikiPageURL(base, titleOrKey string) string {
	titleOrKey = strings.TrimPrefix(strings.TrimSpace(titleOrKey), "/wiki/")
	titleOrKey = strings.ReplaceAll(titleOrKey, " ", "_")
	if titleOrKey == "" {
		return strings.TrimSuffix(base, "/")
	}
	parts := strings.Split(titleOrKey, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	path := strings.Join(parts, "/")
	if base == "" {
		return "/wiki/" + path
	}
	return strings.TrimSuffix(base, "/") + "/wiki/" + path
}
