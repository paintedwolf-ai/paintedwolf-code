package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func nvdRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "nvd",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "nvd")
			if base == "" {
				return "", fmt.Errorf("nvd endpoint not configured")
			}
			u, err := url.Parse(base + "/rest/json/cves/2.0")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("keywordSearch", query)
			q.Set("resultsPerPage", fmt.Sprintf("%d", min(max, 5)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseNVDHits,
	}
}

func parseNVDHits(body []byte) ([]WebHit, error) {
	var data struct {
		Vulnerabilities []struct {
			CVE struct {
				ID           string `json:"id"`
				Published    string `json:"published"`
				VulnStatus   string `json:"vulnStatus"`
				Descriptions []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"descriptions"`
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Vulnerabilities))
	for _, vulnerability := range data.Vulnerabilities {
		cve := vulnerability.CVE
		id := strings.TrimSpace(cve.ID)
		if id == "" {
			continue
		}
		description := ""
		for _, candidate := range cve.Descriptions {
			if candidate.Lang == "en" {
				description = truncateText(candidate.Value, 300)
				break
			}
		}
		parts := make([]string, 0, 2)
		if status := strings.TrimSpace(cve.VulnStatus); status != "" {
			parts = append(parts, status)
		}
		if description != "" {
			parts = append(parts, description)
		}
		// The disclosure timestamp supplies the structured result date.
		date := ""
		if published := strings.TrimSpace(cve.Published); len(published) >= len(webHitDateLayout) {
			date = published[:len(webHitDateLayout)]
		}
		hits = append(hits, WebHit{
			Title:    id,
			URL:      "https://nvd.nist.gov/vuln/detail/" + url.PathEscape(id),
			Snippet:  strings.Join(parts, " · "),
			Date:     date,
			Provider: "nvd",
		})
	}
	return hits, nil
}
