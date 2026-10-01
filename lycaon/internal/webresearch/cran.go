package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func cranRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "cran",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "cran")
			if base == "" {
				return "", fmt.Errorf("cran endpoint not configured")
			}
			u, err := url.Parse(base + "/package/_search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("size", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCranHits,
	}
}

func parseCranHits(body []byte) ([]WebHit, error) {
	var data struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					Title   string `json:"Title"`
					Version string `json:"Version"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Hits.Hits))
	for _, it := range data.Hits.Hits {
		name := strings.TrimSpace(it.ID)
		if name == "" {
			continue
		}
		snippet := strings.Join(strings.Fields(strings.TrimSpace(it.Source.Title)), " ")
		if ver := strings.TrimSpace(it.Source.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      "https://cran.r-project.org/package=" + url.PathEscape(name),
			Snippet:  snippet,
			Provider: "cran",
		})
	}
	return hits, nil
}
