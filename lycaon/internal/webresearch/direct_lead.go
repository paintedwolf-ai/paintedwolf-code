package webresearch

import (
	"strings"
	"unicode"
)

// seedLead supplies a host to crawl and title words to rank discovered URLs.
type seedLead struct {
	title string
	host  string
}

func normalizeLeadHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimSuffix(raw, "/")
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func leadPublisherBase(lead seedLead) string {
	if h := normalizeLeadHost(lead.host); h != "" {
		return normalizeSiteBase("https://" + h)
	}
	return ""
}

const maxLeadTitleNGrams = 12

// leadTitleNGrams supplies adjacent word pairs and short whole titles for crawl ranking.
// Corpus frequency filters common phrases when they are scored.
func leadTitleNGrams(title string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := tokens[:0]
	for _, tok := range tokens {
		if len(tok) >= 2 {
			kept = append(kept, tok)
		}
	}
	var out []string
	if n := len(kept); n > 0 && n <= 4 {
		out = append(out, strings.Join(kept, " "))
	}
	for i := 0; i+1 < len(kept) && len(out) < maxLeadTitleNGrams; i++ {
		out = append(out, kept[i]+" "+kept[i+1])
	}
	return out
}

func (p *seedPlan) mergeLeadHosts() {
	if len(p.leads) == 0 {
		return
	}
	seen := seedHostsSet(p.seeds)
	for _, lead := range p.leads {
		base := leadPublisherBase(lead)
		if base == "" {
			continue
		}
		host := strings.ToLower(urlHost(base))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		p.seeds = append(p.seeds, base)
		if len(p.seeds) >= maxSeedsPerQuery {
			break
		}
	}
}
