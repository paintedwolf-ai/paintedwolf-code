package evidence

import "strings"

// LedgerHasSurfaceSnapshot reports whether the ledger has a surface_snapshot-shaped record.
func LedgerHasSurfaceSnapshot(ev Ledger) bool {
	for _, rec := range ev.Handles {
		if RecordShape(rec) == ShapeSurfaceSnapshot {
			return true
		}
	}
	return false
}

// LedgerHasVisualIntent reports whether the ledger has a visual-shaped (authored intent) record.
func LedgerHasVisualIntent(ev Ledger) bool {
	for _, rec := range ev.Handles {
		if RecordShape(rec) == ShapeVisual {
			return true
		}
	}
	return false
}

// CitationResolvesToVisualIntent reports whether a citation targets an authored mockup.
func CitationResolvesToVisualIntent(findingHandle string, ev Ledger) bool {
	handle := strings.TrimSpace(findingHandle)
	if handle == "" {
		return false
	}
	rec, ok := ResolveHandle(ev, handle)
	return ok && RecordShape(rec) == ShapeVisual
}
