package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func gitlabRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "gitlab",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "gitlab")
			if base == "" {
				return "", fmt.Errorf("gitlab endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v4/projects")
			if err != nil {
				return "", err
			}
			count := max
			if count > 100 {
				count = 100
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("simple", "true")
			q.Set("order_by", "star_count")
			q.Set("sort", "desc")
			q.Set("per_page", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			if key := strings.TrimSpace(s.Keys["gitlab"]); key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			return nil
		},
		ParseHits: parseGitlabHits,
	}
}

func parseGitlabHits(body []byte) ([]WebHit, error) {
	var data []struct {
		NameWithNamespace string `json:"name_with_namespace"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
		Description       string `json:"description"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data))
	for _, it := range data {
		if it.WebURL == "" {
			continue
		}
		title := it.NameWithNamespace
		if title == "" {
			title = it.PathWithNamespace
		}
		if title == "" {
			title = it.WebURL
		}
		hits = append(hits, WebHit{
			Title:    webindex.NormalizeWebTextForStorage(title, 0),
			URL:      it.WebURL,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Description, 0),
			Provider: "gitlab",
		})
	}
	return hits, nil
}
