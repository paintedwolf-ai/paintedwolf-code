package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func metacpanRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "metacpan",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "metacpan")
			if base == "" {
				return "", fmt.Errorf("metacpan endpoint not configured")
			}
			u, err := url.Parse(base + "/v1/release/_search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("size", fmt.Sprintf("%d", min(max, 25)*2))
			q.Set("_source", "distribution,abstract,version,author")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMetacpanHits,
	}
}

func parseMetacpanHits(body []byte) ([]WebHit, error) {
	var data struct {
		Hits struct {
			Hits []struct {
				Source struct {
					Distribution string `json:"distribution"`
					Abstract     string `json:"abstract"`
					Version      string `json:"version"`
					Author       string `json:"author"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	// Release search returns one hit per release; keep the highest-scored hit
	// per distribution so the same module does not flood the results.
	seen := make(map[string]struct{}, len(data.Hits.Hits))
	hits := make([]WebHit, 0, len(data.Hits.Hits))
	for _, it := range data.Hits.Hits {
		dist := strings.TrimSpace(it.Source.Distribution)
		if dist == "" {
			continue
		}
		if _, dup := seen[dist]; dup {
			continue
		}
		seen[dist] = struct{}{}
		if len(hits) >= 25 {
			break
		}
		snippet := strings.TrimSpace(it.Source.Abstract)
		if ver := strings.TrimSpace(it.Source.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		if author := strings.TrimSpace(it.Source.Author); author != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += author
		}
		hits = append(hits, WebHit{
			Title:    dist,
			URL:      "https://metacpan.org/dist/" + url.PathEscape(dist),
			Snippet:  snippet,
			Provider: "metacpan",
		})
	}
	return hits, nil
}
