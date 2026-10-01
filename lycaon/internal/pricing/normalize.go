package pricing

import (
	"regexp"
	"strings"
)

// Cross-region profiles prefix a geographic token to the base model ID.
var bedrockGeoPrefixes = []string{"us-gov.", "us.", "eu.", "apac.", "jp.", "au.", "ca.", "sa.", "global."}

// dateSuffix matches a trailing YYYYMMDD release stamp for fallback lookups.
var dateSuffix = regexp.MustCompile(`-20\d{6}$`)

// rateKindAliases merges transports that share one billing catalog.
var rateKindAliases = map[string]string{
	"vertex-express": "gemini",
}

// RateKind canonicalizes transports that share billing.
func RateKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if canonical, ok := rateKindAliases[kind]; ok {
		return canonical
	}
	return kind
}

// NormalizeRateModelID strips fixed transport prefixes from a machine ID.
func NormalizeRateModelID(kind, id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	switch RateKind(kind) {
	case "gemini":
		id = strings.TrimPrefix(id, "models/")
	case "vertex":
		// Discovery IDs include a publisher segment; feed keys do not.
		if i := strings.IndexByte(id, '/'); i > 0 && i < len(id)-1 {
			id = id[i+1:]
		}
	case "bedrock":
		for _, p := range bedrockGeoPrefixes {
			if strings.HasPrefix(id, p) {
				id = strings.TrimPrefix(id, p)
				break
			}
		}
	}
	return id
}

// RateLookupCandidates returns ordered exact, variant-free, and date-free IDs.
func RateLookupCandidates(kind, id string) []string {
	canonical := NormalizeRateModelID(kind, id)
	out := make([]string, 0, 3)
	add := func(s string) {
		if s == "" {
			return
		}
		for _, seen := range out {
			if seen == s {
				return
			}
		}
		out = append(out, s)
	}
	add(canonical)
	base := canonical
	if i := strings.IndexByte(base, ':'); i > 0 {
		base = base[:i]
		add(base)
	}
	add(dateSuffix.ReplaceAllString(base, ""))
	return out
}
