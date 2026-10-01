package evidence

import "strings"

// ResolveObservation binds an explicit handle to its captured observation, even
// after another observation replaces it for unqualified path lookup. A supplied
// handle is provenance, not a hint permitting substitution of another record.
func ResolveObservation(roots CitationRoots, ev Ledger, handle string, triple Triple) Resolution {
	out := Resolution{Handle: handle, Line: triple.Line, Excerpt: strings.TrimSpace(triple.Excerpt), Verdict: VerdictUnverifiable}
	rec, ok := ResolveHandle(ev, handle)
	if !ok {
		return out
	}
	out.Path = rec.Path
	if strings.TrimSpace(triple.Path) != "" {
		paths := make(map[string][]string)
		for _, path := range IndexedPathsForRecord(rec) {
			paths[path] = []string{handle}
		}
		path := LedgerPathKey(roots, triple.Path, paths)
		if path == "" || len(paths[path]) == 0 {
			out.Path = NormalizeResolvePath(roots.ProjectDir, triple.Path)
			return out
		}
		out.Path = path
	}
	matched, _ := VerifyRecord(rec, Claim{Handle: handle, Path: out.Path, Line: out.Line, Excerpt: out.Excerpt})
	if matched {
		out.Verdict = VerdictMatched
		if out.Excerpt == "" {
			out.Verdict = VerdictTraced
		}
	} else if out.Excerpt != "" && observationAllowsTrace(rec) {
		// Keep the existing paraphrase verdict, but never trace text the ledger
		// can attribute to a different observation onto this explicit handle.
		for other := range ev.Handles {
			if other != handle && ExcerptMatchesHandle(ev, other, out.Line, out.Excerpt) {
				return out
			}
		}
		out.Verdict = VerdictTraced
	}
	return out
}

func observationAllowsTrace(rec Record) bool {
	switch RecordShape(rec) {
	case ShapeFileRegion, ShapeCommand, ShapeOpaque:
		return true
	default:
		return false
	}
}
