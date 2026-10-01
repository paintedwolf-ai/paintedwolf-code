package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

const openAlexPoliteMailto = politeContactMailto

func openalexRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "openalex",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "openalex")
			if base == "" {
				return "", fmt.Errorf("openalex endpoint not configured")
			}
			u, err := url.Parse(base + "/works")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("per_page", fmt.Sprintf("%d", count))
			q.Set("mailto", openAlexPoliteMailto)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseOpenAlexHits,
	}
}

func parseOpenAlexHits(body []byte) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Title           string `json:"title"`
			DisplayName     string `json:"display_name"`
			PublicationYear int    `json:"publication_year"`
			DOI             string `json:"doi"`
			PrimaryLocation *struct {
				LandingPageURL string `json:"landing_page_url"`
			} `json:"primary_location"`
			OpenAccess *struct {
				OAURL string `json:"oa_url"`
			} `json:"open_access"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
		hitURL := ""
		if it.PrimaryLocation != nil {
			hitURL = strings.TrimSpace(it.PrimaryLocation.LandingPageURL)
		}
		if hitURL == "" && it.OpenAccess != nil {
			hitURL = strings.TrimSpace(it.OpenAccess.OAURL)
		}
		if hitURL == "" {
			hitURL = strings.TrimSpace(it.DOI)
		}
		if hitURL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = webindex.NormalizeWebTextForStorage(it.DisplayName, 0)
		}
		if title == "" {
			title = hitURL
		}
		snippet := ""
		if it.PublicationYear > 0 {
			snippet = fmt.Sprintf("%d", it.PublicationYear)
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: "openalex",
		})
	}
	return hits, nil
}
