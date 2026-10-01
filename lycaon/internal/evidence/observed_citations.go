package evidence

import (
	"fmt"
	"sort"
	"strings"
)

const (
	ObservedCitationKindPathLine     = "path_line"
	ObservedCitationKindBacktickPath = "backtick_path"
	ObservedCitationKindURL          = "url"
)

// ObservedCitations enumerates normalized ledger tokens eligible for prose membership tests.
func ObservedCitations(ev Ledger) []ObservedCitation {
	if len(ev.Handles) == 0 {
		return nil
	}
	fileNorm := FileRegionNormalizer{}
	var out []ObservedCitation
	seen := map[string]struct{}{}
	add := func(c ObservedCitation) {
		c.Token = strings.TrimSpace(c.Token)
		if c.Token == "" {
			return
		}
		key := c.Kind + "|" + c.Token
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	for _, handle := range HandlesSorted(ev) {
		rec, ok := ev.Handles[handle]
		if !ok {
			continue
		}
		tier := RecordFidelity(rec, ev, handle)
		for _, u := range ObservedURLsForRecord(rec) {
			add(ObservedCitation{Token: u, URL: u, Kind: ObservedCitationKindURL, Fidelity: tier})
		}
		pathLineSeen := map[string]struct{}{}
		for path, lines := range rec.grepLines {
			path = fileNorm.Normalize(path)
			ptier := pathTrustOnRecord(rec, path)
			if ptier == FidelityStructured {
				ptier = tier
			}
			for line := range lines {
				if line <= 0 {
					continue
				}
				token := pathLineCitationKey(path, line)
				pathLineSeen[path+"|"+token] = struct{}{}
				add(ObservedCitation{
					Token:    token,
					Path:     path,
					Line:     line,
					Kind:     ObservedCitationKindPathLine,
					Fidelity: ptier,
				})
			}
		}
		for _, path := range IndexedPathsForRecord(rec) {
			path = fileNorm.Normalize(path)
			ptier := pathTrustOnRecord(rec, path)
			if ev.PathFidelity != nil {
				if t, ok := ev.PathFidelity[path]; ok && t != "" {
					ptier = t
				}
			}
			hasLines := false
			if len(rec.LineRanges) > 0 {
				for _, rng := range rec.LineRanges {
					for line := rng.Start; line <= rng.End; line++ {
						token := pathLineCitationKey(path, line)
						if _, ok := pathLineSeen[path+"|"+token]; ok {
							continue
						}
						pathLineSeen[path+"|"+token] = struct{}{}
						hasLines = true
						add(ObservedCitation{
							Token:    token,
							Path:     path,
							Line:     line,
							Kind:     ObservedCitationKindPathLine,
							Fidelity: ptier,
						})
					}
				}
			}
			if hasLines || !observedPathEligibleForProse(path) {
				continue
			}
			add(ObservedCitation{
				Token:    path,
				Path:     path,
				Kind:     ObservedCitationKindBacktickPath,
				Fidelity: ptier,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Token < out[j].Token
	})
	return out
}

func observedPathEligibleForProse(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return false
	}
	if len(path) < 3 {
		return false
	}
	return true
}

func pathLineCitationKey(path string, line int) string {
	return fmt.Sprintf("%s:%d", NormalizeLedgerPath(path), line)
}
