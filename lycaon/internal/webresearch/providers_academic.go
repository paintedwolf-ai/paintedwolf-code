package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
)

func arxivRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "arxiv",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "arxiv")
			if base == "" {
				return "", fmt.Errorf("arxiv endpoint not configured")
			}
			u, err := url.Parse(base + "/api/query")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("search_query", "all:"+query)
			q.Set("max_results", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			return parseAtomHits(body, "arxiv", 300)
		},
	}
}

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

func openLibraryRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "openlibrary",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "openlibrary")
			if base == "" {
				return "", fmt.Errorf("openlibrary endpoint not configured")
			}
			u, err := url.Parse(base + "/search.json")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("limit", fmt.Sprintf("%d", min(max, 25)))
			q.Set("fields", "key,title,author_name,first_publish_year")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: parseOpenLibraryHits,
	}
}

func parseOpenLibraryHits(body []byte, s Settings) ([]WebHit, error) {
	var data struct {
		Docs []struct {
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			AuthorName       []string `json:"author_name"`
			FirstPublishYear int      `json:"first_publish_year"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, "openlibrary"), "/")
	if base == "" {
		base = "https://openlibrary.org"
	}
	hits := make([]WebHit, 0, len(data.Docs))
	for _, it := range data.Docs {
		key := strings.Trim(strings.TrimSpace(it.Key), "/")
		title := strings.TrimSpace(it.Title)
		if key == "" || title == "" {
			continue
		}
		parts := make([]string, 0, 2)
		if len(it.AuthorName) > 0 {
			parts = append(parts, strings.Join(it.AuthorName, ", "))
		}
		if it.FirstPublishYear > 0 {
			parts = append(parts, fmt.Sprintf("%d", it.FirstPublishYear))
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      base + "/" + key,
			Snippet:  strings.Join(parts, " · "),
			Provider: "openlibrary",
		})
	}
	return hits, nil
}
