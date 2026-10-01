package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const cratesIOUserAgent = providerSearchUserAgent + " (https://github.com/paintedwolf-ai/paintedwolf-code)"

func cratesIoRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "crates_io",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return cratesIOSearchURL(s, "crates_io", query, max)
		},
		BuildRequest: cratesIOBuildRequest,
		ParseHits:    parseCratesIOHits,
	}
}

func cratesIOBuildRequest(req *http.Request, _ Settings) error {
	req.Header.Set("User-Agent", cratesIOUserAgent)
	return nil
}

func cratesIOSearchURL(s Settings, providerID, query string, max int) (string, error) {
	base := keylessEndpoint(s, providerID)
	if base == "" {
		return "", fmt.Errorf("%s endpoint not configured", providerID)
	}
	u, err := url.Parse(base + "/api/v1/crates")
	if err != nil {
		return "", err
	}
	count := max
	if count > 25 {
		count = 25
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("per_page", fmt.Sprintf("%d", count))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseCratesIOHits(body []byte) ([]WebHit, error) {
	var data struct {
		Crates []struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Documentation string `json:"documentation"`
			Downloads     int64  `json:"downloads"`
		} `json:"crates"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Crates)*2)
	for _, it := range data.Crates {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		snippet := strings.TrimSpace(it.Description)
		if it.Downloads > 0 {
			if snippet != "" {
				snippet += " · "
			}
			snippet += fmt.Sprintf("%d downloads", it.Downloads)
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      "https://crates.io/crates/" + url.PathEscape(name),
			Snippet:  snippet,
			Provider: "crates_io",
		})
		hits = append(hits, WebHit{
			Title:    name + " documentation",
			URL:      docsRsCrateURL(name, it.Documentation),
			Snippet:  strings.TrimSpace(it.Description),
			Provider: "crates_io",
		})
	}
	return hits, nil
}

func docsRsCrateURL(name, documentation string) string {
	doc := strings.TrimSpace(documentation)
	if strings.Contains(doc, "docs.rs/") {
		return doc
	}
	return "https://docs.rs/" + url.PathEscape(name)
}
