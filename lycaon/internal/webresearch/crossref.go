package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func crossrefRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "crossref",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "crossref")
			if base == "" {
				return "", fmt.Errorf("crossref endpoint not configured")
			}
			u, err := url.Parse(base + "/works")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("query", query)
			q.Set("rows", fmt.Sprintf("%d", min(max, 25)))
			q.Set("select", "title,DOI,URL,container-title,published")
			q.Set("mailto", politeContactMailto)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCrossrefHits,
	}
}

func parseCrossrefHits(body []byte) ([]WebHit, error) {
	var data struct {
		Message struct {
			Items []struct {
				Title          []string `json:"title"`
				DOI            string   `json:"DOI"`
				URL            string   `json:"URL"`
				ContainerTitle []string `json:"container-title"`
				Published      struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Message.Items))
	for _, it := range data.Message.Items {
		title := ""
		if len(it.Title) > 0 {
			title = webindex.NormalizeWebTextForStorage(it.Title[0], 0)
		}
		hitURL := strings.TrimSpace(it.URL)
		if hitURL == "" && strings.TrimSpace(it.DOI) != "" {
			hitURL = "https://doi.org/" + strings.TrimSpace(it.DOI)
		}
		if title == "" || hitURL == "" {
			continue
		}
		parts := make([]string, 0, 2)
		if len(it.ContainerTitle) > 0 {
			if c := strings.TrimSpace(it.ContainerTitle[0]); c != "" {
				parts = append(parts, c)
			}
		}
		if len(it.Published.DateParts) > 0 && len(it.Published.DateParts[0]) > 0 {
			if year := it.Published.DateParts[0][0]; year > 0 {
				parts = append(parts, fmt.Sprintf("%d", year))
			}
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.Join(parts, " · "),
			Provider: "crossref",
		})
	}
	return hits, nil
}
