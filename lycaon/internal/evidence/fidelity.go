package evidence

import "strings"

func mergePathFidelity(current, incoming string) string {
	if fidelityRank(current) >= fidelityRank(incoming) {
		return current
	}
	return incoming
}

func fidelityRank(tier string) int {
	switch tier {
	case FidelityStructured:
		return 3
	case FidelityScraped:
		return 2
	case FidelityOpaque:
		return 1
	default:
		return 0
	}
}

// RecordFidelity returns the effective trust tier for a ledger record.
func RecordFidelity(rec Record, ledger Ledger, handle string) string {
	if t := strings.TrimSpace(rec.Fidelity); t != "" {
		return t
	}
	if t := pathTrustForHandle(ledger, handle, rec); t != "" {
		return t
	}
	return FidelityForShape(RecordShape(rec))
}

func pathTrustForHandle(ledger Ledger, handle string, rec Record) string {
	if len(ledger.PathFidelity) == 0 {
		return ""
	}
	best := ""
	for _, path := range IndexedPathsForRecord(rec) {
		if t, ok := ledger.PathFidelity[path]; ok {
			best = mergePathFidelity(best, t)
		}
	}
	return best
}

// FidelityForShape returns the default capture tier for a shape.
func FidelityForShape(shape string) string {
	switch strings.TrimSpace(shape) {
	case ShapeFileRegion, ShapeArtifact, ShapeVisual, ShapePageGeometry, ShapeStructuredEvent:
		return FidelityStructured
	case ShapeURL, ShapeSurfaceSnapshot:
		return FidelityScraped
	case ShapeCommand, ShapeOpaque:
		return FidelityOpaque
	default:
		return FidelityOpaque
	}
}
