package webresearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
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

func wikidataRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "wikidata",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "wikidata")
			if base == "" {
				return "", fmt.Errorf("wikidata endpoint not configured")
			}
			u, err := url.Parse(base + "/w/api.php")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("action", "wbsearchentities")
			q.Set("search", query)
			q.Set("language", "en")
			q.Set("uselang", "en")
			q.Set("format", "json")
			q.Set("limit", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseWikidataHits,
	}
}

func parseWikidataHits(body []byte) ([]WebHit, error) {
	var data struct {
		Search []struct {
			ID          string `json:"id"`
			ConceptURI  string `json:"concepturi"`
			URL         string `json:"url"`
			Label       string `json:"label"`
			Description string `json:"description"`
			Display     struct {
				Label       struct{ Value string } `json:"label"`
				Description struct{ Value string } `json:"description"`
			} `json:"display"`
		} `json:"search"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Search))
	for _, it := range data.Search {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			continue
		}
		title := strings.TrimSpace(it.Display.Label.Value)
		if title == "" {
			title = strings.TrimSpace(it.Label)
		}
		if title == "" {
			title = id
		}
		snippet := strings.TrimSpace(it.Display.Description.Value)
		if snippet == "" {
			snippet = strings.TrimSpace(it.Description)
		}
		hits = append(hits, WebHit{
			Title:    fmt.Sprintf("%s (%s)", title, id),
			URL:      wikidataEntityURL(it.ConceptURI, it.URL, id),
			Snippet:  snippet,
			Provider: "wikidata",
		})
	}
	return hits, nil
}

func wikidataEntityURL(conceptURI, protocolRelative, id string) string {
	if u := strings.TrimSpace(conceptURI); u != "" {
		return u
	}
	if u := strings.TrimSpace(protocolRelative); strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return "https://www.wikidata.org/wiki/" + url.PathEscape(id)
}

func internetArchiveRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "internetarchive",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "internetarchive")
			if base == "" {
				return "", fmt.Errorf("internetarchive endpoint not configured")
			}
			u, err := url.Parse(base + "/advancedsearch.php")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Add("fl[]", "identifier")
			q.Add("fl[]", "title")
			q.Add("fl[]", "description")
			q.Set("rows", fmt.Sprintf("%d", min(max, 25)))
			q.Set("output", "json")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseInternetArchiveHits,
	}
}

func parseInternetArchiveHits(body []byte) ([]WebHit, error) {
	var data struct {
		Response struct {
			Docs []struct {
				Identifier  string            `json:"identifier"`
				Title       archiveFlexString `json:"title"`
				Description archiveFlexString `json:"description"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Response.Docs))
	for _, it := range data.Response.Docs {
		id := strings.TrimSpace(it.Identifier)
		if id == "" {
			continue
		}
		title := strings.TrimSpace(string(it.Title))
		if title == "" {
			title = id
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      "https://archive.org/details/" + url.PathEscape(id),
			Snippet:  truncateText(webindex.NormalizeWebTextForStorage(string(it.Description), 0), 300),
			Provider: "internetarchive",
		})
	}
	return hits, nil
}

// Metadata fields accept either a string or an array of strings.
type archiveFlexString string

func (f *archiveFlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*f = archiveFlexString(strings.Join(arr, " "))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*f = archiveFlexString(s)
	return nil
}
