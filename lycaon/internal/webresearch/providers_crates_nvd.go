package webresearch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
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

// keylessEndpoint returns the configured base URL for a provider that needs no
// API key, with any trailing slash trimmed.
func keylessEndpoint(s Settings, providerID string) string {
	return strings.TrimSuffix(strings.TrimSpace(s.Config[providerID]["endpoint"]), "/")
}

// politeContactMailto identifies this client to APIs that request contact details.
const politeContactMailto = "search@paint-wolf-code.com"

const (
	// maxSeedsPerQuery caps the merged publisher-root set (plan seeds plus lead
	// hosts) crawled for one background warmSeed / pickSeeds call.
	maxSeedsPerQuery = 40
	maxLeadsPerQuery = 40
	seedExpandMin    = 4
	seedExpandMax    = 8
)

// seedBudget scales discovery breadth while limiting duplicate seed hosts.
type seedBudget struct {
	maxSeeds  int
	minLeads  int
	maxLeads  int
	expandMin int
	expandMax int
	hitTarget int
}

func seedBudgetForHits(maxResults int) seedBudget {
	if maxResults <= 0 {
		maxResults = defaultPageBudget
	}
	scale := max(maxResults, 8)
	// Lead count bounds completion size within the seed-call deadline.
	return seedBudget{
		maxSeeds:  max(3, scale/3),
		minLeads:  max(8, scale-2),
		maxLeads:  min(maxLeadsPerQuery, max(12, scale+2)),
		expandMin: seedExpandMin,
		expandMax: seedExpandMax,
		hitTarget: maxResults,
	}
}

// slimSeedBudget reduces discovery breadth for the shorter retry deadline.
func slimSeedBudget(maxResults int) seedBudget {
	if maxResults <= 0 {
		maxResults = defaultPageBudget
	}
	return seedBudget{
		maxSeeds:  2,
		minLeads:  6,
		maxLeads:  10,
		expandMin: 3,
		expandMax: 5,
		hitTarget: maxResults,
	}
}

func buildPickSeedsUserMessage(ctx context.Context, query string, period Period, taskHint string, budget seedBudget) (string, error) {
	return guidance.RenderCatalog(ctx, guidance.UtilityPickSeedsUserRef, map[string]any{
		"query":      strings.TrimSpace(query),
		"min_leads":  budget.minLeads,
		"max_leads":  budget.maxLeads,
		"max_seeds":  budget.maxSeeds,
		"expand_min": budget.expandMin,
		"expand_max": budget.expandMax,
		"hit_target": budget.hitTarget,
		// The explicit period controls recency, including queries containing a year.
		"period_current": period.IsCurrent(),
		"period":         period.String(),
		"task_hint":      strings.TrimSpace(taskHint),
	})
}

func (p seedPlan) seedHostCount() int {
	hosts := seedHostsSet(p.seeds)
	for _, lead := range p.leads {
		if h := normalizeLeadHost(lead.host); h != "" {
			hosts[h] = struct{}{}
		}
	}
	return len(hosts)
}

// Fewer seed hosts receive a larger share of the hit budget.
func perHostCapForHostCount(hosts int) int {
	switch {
	case hosts <= 2:
		return 5
	case hosts >= 7:
		return 2
	default:
		return maxHitsPerHost
	}
}
