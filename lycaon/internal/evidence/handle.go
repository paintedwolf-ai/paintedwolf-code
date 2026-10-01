package evidence

import (
	"strings"
)

// ResolveHandle returns the ledger record for handle.
func ResolveHandle(ev Ledger, handle string) (Record, bool) {
	handle = strings.TrimSpace(handle)
	if handle == "" || ev.Handles == nil {
		return Record{}, false
	}
	rec, ok := ev.Handles[handle]
	return rec, ok
}

// ResolveHandleToken replaces a leading handle while preserving its suffix.
func ResolveHandleToken(ev Ledger, token string) string {
	m := handleTokenPrefixRE.FindStringSubmatch(token)
	if m == nil {
		return token
	}
	rec, ok := ResolveHandle(ev, m[1])
	if !ok || strings.TrimSpace(rec.Path) == "" {
		return token
	}
	return rec.Path + m[2]
}

// ResolveHandleTokens maps ResolveHandleToken across display tokens.
func ResolveHandleTokens(ev Ledger, tokens []string) []string {
	if len(tokens) == 0 {
		return tokens
	}
	out := make([]string, len(tokens))
	for i, token := range tokens {
		out[i] = ResolveHandleToken(ev, token)
	}
	return out
}

// HandleForPath returns the newest handle for path limited to the given kinds.
// When kinds is empty, any kind matches.
func HandleForPath(ev Ledger, path string, kinds ...string) (string, bool) {
	path = NormalizeLedgerPath(path)
	if path == "" || ev.ByPath == nil {
		return "", false
	}
	handles := ev.ByPath[path]
	if len(handles) == 0 {
		return "", false
	}
	kindSet := kindFilterSet(kinds)
	for i := len(handles) - 1; i >= 0; i-- {
		handle := handles[i]
		rec, ok := ev.Handles[handle]
		if !ok {
			continue
		}
		if len(kindSet) > 0 {
			if _, ok := kindSet[rec.Kind]; !ok {
				continue
			}
		}
		return handle, true
	}
	return "", false
}

func kindFilterSet(kinds []string) map[string]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		k = strings.TrimSpace(strings.ToLower(k))
		if k != "" {
			out[k] = struct{}{}
		}
	}
	return out
}

// LineInRanges reports whether line falls in any inclusive ReadRange for the path.
func LineInRanges(line int, ranges []LineRange) bool {
	if line <= 0 {
		return false
	}
	for _, r := range ranges {
		if line >= r.Start && line <= r.End {
			return true
		}
	}
	return false
}

// ExcerptMatchesHandle reports whether excerpt is a verbatim substring of the handle body
// and line falls within recorded ranges when any are set.
func ExcerptMatchesHandle(ev Ledger, handle string, line int, excerpt string) bool {
	ok, _ := VerifyHandle(ev, handle, Claim{
		Handle:  handle,
		Line:    line,
		Excerpt: excerpt,
	})
	return ok
}

// NamespaceLedger prefixes every handle with legID for synthesis union merges.
func NamespaceLedger(ev Ledger, legID string) Ledger {
	legID = strings.TrimSpace(legID)
	if legID == "" {
		return ev
	}
	out := InitLedger()
	out.handleOrder = append([]string(nil), ev.handleOrder...)
	handleMap := make(map[string]string, len(ev.Handles))
	for handle, rec := range ev.Handles {
		prefixed := legID + ":" + handle
		cloned := cloneRecord(rec)
		cloned.Handle = prefixed
		if cloned.SupersededBy != "" {
			cloned.SupersededBy = legID + ":" + cloned.SupersededBy
		}
		out.Handles[prefixed] = cloned
		handleMap[handle] = prefixed
	}
	out.handleOrder = make([]string, 0, len(ev.handleOrder))
	for _, handle := range ev.handleOrder {
		if prefixed, ok := handleMap[handle]; ok {
			out.handleOrder = append(out.handleOrder, prefixed)
		}
	}
	for path, handles := range ev.ByPath {
		for _, handle := range handles {
			if prefixed, ok := handleMap[handle]; ok {
				out.ByPath[path] = append(out.ByPath[path], prefixed)
			}
		}
	}
	for path, tier := range ev.PathFidelity {
		out.PathFidelity[path] = mergePathFidelity(out.PathFidelity[path], tier)
	}
	return out
}

func cloneRecord(rec Record) Record {
	out := rec
	if len(rec.pathsTouched) > 0 {
		out.pathsTouched = append([]string(nil), rec.pathsTouched...)
	}
	if len(rec.urlsTouched) > 0 {
		out.urlsTouched = append([]string(nil), rec.urlsTouched...)
	}
	if rec.urlTitles != nil {
		out.urlTitles = make(map[string]string, len(rec.urlTitles))
		for u, t := range rec.urlTitles {
			out.urlTitles[u] = t
		}
	}
	if len(rec.LineRanges) > 0 {
		out.LineRanges = append([]LineRange(nil), rec.LineRanges...)
	}
	if len(rec.Body) > 0 {
		out.Body = append([]string(nil), rec.Body...)
	}
	if rec.grepLines != nil {
		out.grepLines = make(map[string]map[int]string, len(rec.grepLines))
		for path, byLine := range rec.grepLines {
			cloned := make(map[int]string, len(byLine))
			for line, content := range byLine {
				cloned[line] = content
			}
			out.grepLines[path] = cloned
		}
	}
	if rec.pathTiers != nil {
		out.pathTiers = make(map[string]string, len(rec.pathTiers))
		for path, tier := range rec.pathTiers {
			out.pathTiers[path] = tier
		}
	}
	return out
}
