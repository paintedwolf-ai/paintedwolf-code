package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

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
