package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func europepmcRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "europepmc",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "europepmc")
			if base == "" {
				return "", fmt.Errorf("europepmc endpoint not configured")
			}
			u, err := url.Parse(base + "/europepmc/webservices/rest/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("query", query)
			q.Set("format", "json")
			q.Set("pageSize", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseEuropePMCHits,
	}
}

func parseEuropePMCHits(body []byte) ([]WebHit, error) {
	var data struct {
		ResultList struct {
			Result []struct {
				ID           string `json:"id"`
				Source       string `json:"source"`
				DOI          string `json:"doi"`
				Title        string `json:"title"`
				AuthorString string `json:"authorString"`
				JournalTitle string `json:"journalTitle"`
				PubYear      string `json:"pubYear"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.ResultList.Result))
	for _, it := range data.ResultList.Result {
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			continue
		}
		hitURL := europePMCArticleURL(it.Source, it.ID, it.DOI)
		if hitURL == "" {
			continue
		}
		parts := make([]string, 0, 3)
		if j := strings.TrimSpace(it.JournalTitle); j != "" {
			parts = append(parts, j)
		}
		if y := strings.TrimSpace(it.PubYear); y != "" {
			parts = append(parts, y)
		}
		if a := strings.TrimSpace(it.AuthorString); a != "" {
			parts = append(parts, truncateText(a, 80))
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.Join(parts, " · "),
			Provider: "europepmc",
		})
	}
	return hits, nil
}

func europePMCArticleURL(source, id, doi string) string {
	source = strings.TrimSpace(source)
	id = strings.TrimSpace(id)
	if source != "" && id != "" {
		return "https://europepmc.org/article/" + url.PathEscape(source) + "/" + url.PathEscape(id)
	}
	if d := strings.TrimSpace(doi); d != "" {
		return "https://doi.org/" + d
	}
	return ""
}
