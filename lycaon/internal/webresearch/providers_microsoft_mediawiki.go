package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
)

const (
	microsoftLearnMCPPath       = "/api/mcp"
	microsoftLearnSearchTool    = "microsoft_docs_search"
	microsoftLearnAcceptHeader  = "application/json, text/event-stream"
	microsoftLearnSnippetMaxLen = 300
)

func microsoftLearnRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "microsoft_learn",
		Method:      http.MethodPost,
		ContentType: "application/json",
		TimeoutSec:  15,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "microsoft_learn")
			if base == "" {
				return "", fmt.Errorf("microsoft_learn endpoint not configured")
			}
			return strings.TrimSuffix(base, "/") + microsoftLearnMCPPath, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			return surveyjson.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params": map[string]any{
					"name": microsoftLearnSearchTool,
					"arguments": map[string]any{
						"query": query,
					},
				},
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("Accept", microsoftLearnAcceptHeader)
			return nil
		},
		ParseHits: parseMicrosoftLearnHits,
	}
}

func parseMicrosoftLearnHits(body []byte) ([]WebHit, error) {
	payload, err := microsoftLearnSSEPayload(body)
	if err != nil {
		return nil, err
	}
	var data struct {
		Results []struct {
			Title      string `json:"title"`
			Content    string `json:"content"`
			ContentURL string `json:"contentUrl"`
		} `json:"results"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(data.Results))
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
		hitURL := strings.TrimSpace(it.ContentURL)
		if hitURL == "" {
			continue
		}
		if _, dup := seen[hitURL]; dup {
			continue
		}
		seen[hitURL] = struct{}{}
		title := strings.TrimSpace(it.Title)
		if title == "" {
			title = hitURL
		}
		snippet := truncateText(strings.TrimSpace(it.Content), microsoftLearnSnippetMaxLen)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: "microsoft_learn",
		})
	}
	return hits, nil
}

func microsoftLearnSSEPayload(body []byte) ([]byte, error) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" {
			continue
		}
		var envelope struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			continue
		}
		if envelope.Error != nil {
			msg := strings.TrimSpace(envelope.Error.Message)
			if msg == "" {
				msg = "microsoft learn MCP error"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		for _, part := range envelope.Result.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				return []byte(part.Text), nil
			}
		}
	}
	return nil, fmt.Errorf("microsoft learn MCP response missing search payload")
}

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
