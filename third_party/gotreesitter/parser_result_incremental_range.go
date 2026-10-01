package gotreesitter

// languageUsesRangeLimitedResultCompatibility reports whether a language's
// result-compatibility normalizers are proven node-local and therefore safe to
// confine to the reparsed spans of an incremental parse (campaign O(edit),
// spec.campaign.oedit). It is fail-closed: only languages whose every normalizer
// reached from normalizeResultCompatibility reads inside a single node (never a
// parent/sibling relationship the edit could change) are listed. Every other
// language keeps the full-tree walk unchanged.
//
// Go qualifies: its walk normalizers (semicolon-container drop, adjacent-sibling
// boundary, statement-list trailing extras) each read only a node's own children
// and source span, and the root — which always overlaps any range — re-runs the
// only cross-sibling boundary work at the top level. See the normalizer
// classification in the campaign report.
func languageUsesRangeLimitedResultCompatibility(lang *Language) bool {
	if lang == nil {
		return false
	}
	return lang.Name == "go"
}

// incrementalReparsedTopLevelRanges computes the reparsed byte span(s) of an
// incremental parse for range-limited result normalization (campaign O(edit),
// spec.campaign.oedit).
//
// A reused subtree keeps its original (borrowed) arena; a node built fresh in
// THIS parse lives in the primary arena that also owns the freshly-built root.
// So a top-level child whose ownerArena is the primary arena was rebuilt this
// pass, and one in a borrowed arena is a byte-identical subtree lifted from the
// old tree (already normalized when the old tree was built with the same code).
// The reparsed range is the envelope of each contiguous run of freshly-built
// top-level children.
//
// The result is a SOUND SUPERSET of every freshly-built node. The root always
// overlaps any range (it spans the whole file) so its own child-boundary
// normalization runs in full; every other freshly-built node descends from a
// freshly-built top-level child, because reuse is subtree-atomic (a reused
// top-level child has no freshly-built descendant), so it lies inside a returned
// range. Reused top-level children outside the ranges are skipped, which is the
// O(edit) win.
//
// Returns nil when no reuse happened (hasReuse false) or nothing at the top
// level was reused; nil restores the full walk, so the fallback is always safe.
func incrementalReparsedTopLevelRanges(root *Node, primary *nodeArena, hasReuse bool) []Range {
	if root == nil || primary == nil || !hasReuse {
		return nil
	}
	childCount := resultChildCount(root)
	if childCount == 0 {
		return nil
	}
	var ranges []Range
	runActive := false
	reusedSeen := false
	var runLo, runHi uint32
	closeRun := func() {
		if runActive {
			ranges = append(ranges, Range{StartByte: runLo, EndByte: runHi})
			runActive = false
		}
	}
	for i := 0; i < childCount; i++ {
		c := resultChildAt(root, i)
		if c == nil {
			continue
		}
		if c.ownerArena == primary {
			if !runActive {
				runLo, runHi = c.startByte, c.endByte
				runActive = true
			} else {
				if c.startByte < runLo {
					runLo = c.startByte
				}
				if c.endByte > runHi {
					runHi = c.endByte
				}
			}
			continue
		}
		// A reused (borrowed-arena) child ends the current fresh run and proves
		// there is content the walk may skip.
		reusedSeen = true
		closeRun()
	}
	closeRun()
	if !reusedSeen {
		// Every top-level child was rebuilt this pass: a full walk is already
		// O(edit) == O(file) and range-limiting can only add cost. Keep the
		// unchanged full walk.
		return nil
	}
	return ranges
}
