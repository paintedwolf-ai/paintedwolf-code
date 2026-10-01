package webresearch

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// dropIncompleteTail removes the last seed and last lead of a partial plan —
// on a mid-stream buffer they may be truncated fragments.
func dropIncompleteTail(p seedPlan) seedPlan {
	if len(p.seeds) > 0 {
		p.seeds = p.seeds[:len(p.seeds)-1]
	}
	if len(p.leads) > 0 {
		p.leads = p.leads[:len(p.leads)-1]
	}
	return p
}

func parseSeedsJSON(raw string) (seedPlan, error) {
	var plan seedPlan
	var candidates []string
	if obj := extractJSONObject(raw); obj != "" {
		var payload struct {
			Seeds  []string `json:"seeds"`
			Fresh  bool     `json:"fresh"`
			Expand []string `json:"expand"`
			Leads  []struct {
				Title string `json:"title"`
				Host  string `json:"host"`
				// URL absorbs a model-volunteered article path; only its host is
				// used, since result URLs are discovered live.
				URL string `json:"url"`
			} `json:"leads"`
		}
		if err := json.Unmarshal([]byte(obj), &payload); err == nil {
			plan.fresh = payload.Fresh
			plan.expand = payload.Expand
			candidates = payload.Seeds
			for _, lead := range payload.Leads {
				if len(plan.leads) >= maxLeadsPerQuery {
					break
				}
				title := strings.TrimSpace(lead.Title)
				host := normalizeLeadHost(lead.Host)
				if host == "" {
					if u := normalizePageURL(lead.URL); u != "" {
						host = strings.ToLower(urlHost(u))
					}
				}
				if title == "" && host == "" {
					continue
				}
				plan.leads = append(plan.leads, seedLead{title: title, host: host})
			}
		}
	}
	if len(candidates) == 0 {
		// The model ignored the JSON contract (prose, or freestyle keys like
		// "sites") — scrape any bare URLs out of the raw text instead.
		candidates = scrapeURLs(raw)
	}
	seen := make(map[string]struct{})
	for _, s := range candidates {
		seed := normalizeSeed(s)
		if seed == "" {
			continue
		}
		key := strings.ToLower(seed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		plan.seeds = append(plan.seeds, seed)
		if len(plan.seeds) >= maxSeedsPerQuery {
			break
		}
	}
	// Leads alone are a valid plan: mergeLeadHosts turns lead hosts into seeds,
	// and the prompt asks for seeds only as roots not already covered by a lead.
	if len(plan.seeds) == 0 && len(plan.leads) == 0 {
		return seedPlan{}, fmt.Errorf("no valid seeds")
	}
	return plan, nil
}

func normalizeSeed(raw string) string {
	page := normalizePageURL(raw)
	if page == "" {
		return ""
	}
	u, err := url.Parse(page)
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return normalizeSiteBase(page)
	}
	return page
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		if end := strings.LastIndex(raw, "```"); end >= 0 {
			raw = raw[:end]
		}
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(raw, "}")
	if end > start {
		return raw[start : end+1]
	}
	// Small models drop trailing closers or get cut by token limits — repair.
	return balanceJSON(strings.TrimSpace(raw[start:]))
}

// balanceJSON appends the closers a truncated JSON document is missing.
func balanceJSON(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	var b strings.Builder
	b.WriteString(s)
	if inStr {
		b.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
	}
	return b.String()
}

var scrapeURLRe = regexp.MustCompile(`https?://[^\s"'<>\\)\]}]+`)

// scrapeURLs pulls bare URLs out of prose when the model ignored the JSON contract.
func scrapeURLs(raw string) []string {
	matches := scrapeURLRe.FindAllString(raw, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimRight(m, ".,;:!?"))
	}
	return out
}
