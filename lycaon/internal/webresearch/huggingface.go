package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func huggingfaceRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "huggingface",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "huggingface")
			if base == "" {
				return "", fmt.Errorf("huggingface endpoint not configured")
			}
			u, err := url.Parse(base + "/api/models")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: parseHuggingFaceHits,
	}
}

func parseHuggingFaceHits(body []byte, s Settings) ([]WebHit, error) {
	var items []struct {
		ID          string `json:"id"`
		ModelID     string `json:"modelId"`
		PipelineTag string `json:"pipeline_tag"`
		Downloads   int    `json:"downloads"`
		Likes       int    `json:"likes"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, "huggingface"), "/")
	hits := make([]WebHit, 0, len(items))
	for _, it := range items {
		modelID := strings.TrimSpace(it.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(it.ID)
		}
		if modelID == "" {
			continue
		}
		title := modelID
		hitURL := huggingFaceModelURL(base, modelID)
		snippetParts := make([]string, 0, 3)
		if tag := strings.TrimSpace(it.PipelineTag); tag != "" {
			snippetParts = append(snippetParts, tag)
		}
		if it.Downloads > 0 {
			snippetParts = append(snippetParts, fmt.Sprintf("%d downloads", it.Downloads))
		}
		if it.Likes > 0 {
			snippetParts = append(snippetParts, fmt.Sprintf("%d likes", it.Likes))
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.Join(snippetParts, " · "),
			Provider: "huggingface",
		})
	}
	return hits, nil
}

func huggingFaceModelURL(base, modelID string) string {
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), "/")
	if base == "" {
		return "https://huggingface.co/" + modelID
	}
	return base + "/" + modelID
}
