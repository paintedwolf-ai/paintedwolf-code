package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
)

func cssTricksRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "css_tricks",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "css_tricks")
			if base == "" {
				return "", fmt.Errorf("css_tricks endpoint not configured")
			}
			u, err := url.Parse(base + "/wp-json/wp/v2/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("per_page", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCSSTricksHits,
	}
}

func parseCSSTricksHits(body []byte) ([]WebHit, error) {
	var items []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(items))
	for _, item := range items {
		hitURL := strings.TrimSpace(item.URL)
		if hitURL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(item.Title, 0)
		if title == "" {
			title = hitURL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.TrimSpace(item.Subtype),
			Provider: "css_tricks",
		})
	}
	return hits, nil
}

func mankierRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "mankier",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "mankier")
			if base == "" {
				return "", fmt.Errorf("mankier endpoint not configured")
			}
			u, err := url.Parse(base + "/api/v2/mans/")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("q", shortenProviderQuery(query))
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMankierHits,
	}
}

func parseMankierHits(body []byte) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Name        string `json:"name"`
			Section     string `json:"section"`
			Description string `json:"description"`
			URL         string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
		hitURL := strings.TrimSpace(it.URL)
		if hitURL == "" {
			continue
		}
		title := strings.TrimSpace(it.Name)
		if sec := strings.TrimSpace(it.Section); sec != "" {
			if title != "" {
				title = title + "(" + sec + ")"
			} else {
				title = sec
			}
		}
		if title == "" {
			title = hitURL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.TrimSpace(it.Description),
			Provider: "mankier",
		})
	}
	return hits, nil
}

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
