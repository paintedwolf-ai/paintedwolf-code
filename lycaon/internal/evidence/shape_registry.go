package evidence

import (
	"strings"
	"sync"
)

var (
	defaultRegistry   *ShapeRegistry
	defaultRegistryMu sync.Once
)

// DefaultShapeRegistry returns the process-wide evidence shape registry.
func DefaultShapeRegistry() *ShapeRegistry {
	defaultRegistryMu.Do(func() {
		defaultRegistry = NewShapeRegistry()
		registerDefaultShapes(defaultRegistry)
	})
	return defaultRegistry
}

// ShapeRegistry holds registered evidence shapes keyed by shape id.
type ShapeRegistry struct {
	shapes map[string]Shape
}

// NewShapeRegistry constructs an empty registry.
func NewShapeRegistry() *ShapeRegistry {
	return &ShapeRegistry{shapes: make(map[string]Shape)}
}

// Register adds or replaces a shape entry.
func (r *ShapeRegistry) Register(shape Shape) {
	if r == nil || shape.ID == "" {
		return
	}
	r.shapes[shape.ID] = shape
}

// Lookup returns the registered shape for id.
func (r *ShapeRegistry) Lookup(id string) (Shape, bool) {
	if r == nil {
		return Shape{}, false
	}
	shape, ok := r.shapes[id]
	return shape, ok
}

// Verify dispatches claim verification to the shape registered for rec.
func (r *ShapeRegistry) Verify(rec Record, claim Claim) (bool, string) {
	shapeID := RecordShape(rec)
	if shapeID == "" {
		return false, ""
	}
	shape, ok := r.Lookup(shapeID)
	if !ok || shape.Verifier == nil {
		return false, ""
	}
	return shape.Verifier.Verify(rec, claim)
}

func registerDefaultShapes(r *ShapeRegistry) {
	identity := identityNormalizer{}
	fileNorm := FileRegionNormalizer{}
	r.Register(Shape{ID: ShapeFileRegion, Verifier: fileRegionVerifier{}, Normalizer: fileNorm})
	r.Register(Shape{ID: ShapeURL, Verifier: urlVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeCommand, Verifier: verbatimShapeVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeArtifact, Verifier: artifactVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeOpaque, Verifier: verbatimShapeVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeStructuredEvent, Verifier: verbatimShapeVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeVisual, Verifier: visualIntentVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapeSurfaceSnapshot, Verifier: surfaceSnapshotVerifier{}, Normalizer: identity})
	r.Register(Shape{ID: ShapePageGeometry, Verifier: pageGeometryVerifier{}, Normalizer: identity})
}

// RecordShape returns the shape stamped on rec at fingerprint time.
func RecordShape(rec Record) string {
	return strings.TrimSpace(rec.Shape)
}

// VerifyRecord verifies claim against a resolved ledger record.
func VerifyRecord(rec Record, claim Claim) (bool, string) {
	return DefaultShapeRegistry().Verify(rec, claim)
}

// VerifyHandle resolves handle in ev and verifies claim via the record's shape.
func VerifyHandle(ev Ledger, handle string, claim Claim) (bool, string) {
	rec, ok := ResolveHandle(ev, handle)
	if !ok {
		return false, ""
	}
	if strings.TrimSpace(claim.Handle) == "" {
		claim.Handle = handle
	}
	return VerifyRecord(rec, claim)
}

// VerifyURLObserved reports whether url appears in the ledger's canonical observed URL set.
func VerifyURLObserved(ev Ledger, url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	_, ok := ObservedURLsFromEvidence(ev)[url]
	return ok
}

// SupersedesPathHandle retires file observations while retaining event receipts.
func SupersedesPathHandle(newRec, oldRec Record) bool {
	binding := ActiveBinding()
	if binding.KindShape(oldRec.Kind) != ShapeFileRegion {
		return false
	}
	if binding.IsMutationKind(newRec.Kind) {
		return true
	}
	if binding.IsMutationKind(oldRec.Kind) {
		return false
	}
	if !UpdatesFileBindings(newRec) {
		return false
	}
	oldPaths := IndexedPathsForRecord(oldRec)
	if len(oldPaths) != 1 || oldPaths[0] != NormalizeLedgerPath(newRec.Path) {
		return false
	}
	// A record without line ranges counts as covered.
	return LineRangesCover(newRec.LineRanges, oldRec.LineRanges)
}

// UpdatesFileBindings distinguishes new file content from surveys and receipts.
func UpdatesFileBindings(rec Record) bool {
	return ActiveBinding().IsMutationKind(rec.Kind) || (!rec.Survey && strings.EqualFold(strings.TrimSpace(rec.Kind), "read"))
}
