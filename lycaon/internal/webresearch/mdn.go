package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func mdnRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "mdn",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "mdn")
			if base == "" {
				return "", fmt.Errorf("mdn endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v1/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("locale", "en-US")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: parseMDNHits,
	}
}

func parseMDNHits(body []byte, s Settings) ([]WebHit, error) {
	var data struct {
		Documents []struct {
			Title   string `json:"title"`
			MDNURL  string `json:"mdn_url"`
			Summary string `json:"summary"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, "mdn"), "/")
	hits := make([]WebHit, 0, len(data.Documents))
	for _, it := range data.Documents {
		path := strings.TrimSpace(it.MDNURL)
		if path == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = path
		}
		hitURL := path
		if strings.HasPrefix(path, "/") && base != "" {
			hitURL = base + path
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Summary, 0),
			Provider: "mdn",
		})
	}
	return hits, nil
}
