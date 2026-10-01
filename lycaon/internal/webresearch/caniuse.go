package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func caniuseRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "caniuse",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "caniuse")
			if base == "" {
				return "", fmt.Errorf("caniuse endpoint not configured")
			}
			u, err := url.Parse(base + "/process/query.php")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("search", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCanIUseHits,
	}
}

func parseCanIUseHits(body []byte) ([]WebHit, error) {
	var data struct {
		FeatureIDs []string `json:"featureIds"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	count := min(len(data.FeatureIDs), 20)
	hits := make([]WebHit, 0, count)
	for _, rawID := range data.FeatureIDs[:count] {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		hits = append(hits, WebHit{
			Title:    strings.ReplaceAll(id, "-", " "),
			URL:      "https://caniuse.com/" + url.PathEscape(id),
			Snippet:  "Browser compatibility reference",
			Provider: "caniuse",
		})
	}
	return hits, nil
}
