package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
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
			return json.Marshal(map[string]any{
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
