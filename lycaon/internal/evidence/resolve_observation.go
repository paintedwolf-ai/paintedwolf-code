package evidence

import "strings"

// ResolveObservation binds an explicit handle to its captured observation, even
// after another observation replaces it for unqualified path lookup. A supplied
// handle is provenance, not a hint permitting substitution of another record.
func ResolveObservation(roots CitationRoots, ev Ledger, handle string, triple Triple) Resolution {
	out := Resolution{Handle: handle, Line: triple.Line, Excerpt: strings.TrimSpace(triple.Excerpt), Verdict: VerdictUnverifiable, Reason: VerifyReasonUnknownHandle}
	rec, ok := ResolveHandle(ev, handle)
	if !ok {
		return out
	}
	out.Reason = ""
	out.Path = rec.Path
	if strings.TrimSpace(triple.Path) != "" {
		paths := make(map[string][]string)
		for _, path := range IndexedPathsForRecord(rec) {
			paths[path] = []string{handle}
		}
		path := LedgerPathKey(roots, triple.Path, paths)
		if path == "" || len(paths[path]) == 0 {
			out.Reason = VerifyReasonSourcePath
			out.Path = NormalizeResolvePath(roots.ProjectDir, triple.Path)
			return out
		}
		out.Path = path
	}
	matched, reason := VerifyRecord(rec, Claim{Handle: handle, Path: out.Path, Line: out.Line, Excerpt: out.Excerpt})
	out.Reason = reason
	if matched {
		out.Verdict = VerdictMatched
		if out.Excerpt == "" {
			out.Verdict = VerdictTraced
		}
	} else if out.Excerpt != "" && out.Line > 0 && reason != VerifyReasonSourceRange && ExcerptMeaningful(out.Excerpt) {
		// Re-anchor within the explicit observation when the excerpt matches
		// exactly one delivered line of this record; zero or multiple matches
		// stay VerdictUnverifiable.
		if anchors := ExcerptAnchors(rec, out.Path, out.Excerpt); len(anchors) == 1 {
			out.LineCorrectedFrom = out.Line
			out.Line = anchors[0]
			out.Reason = ""
			out.Verdict = VerdictMatched
		}
	}
	return out
}
