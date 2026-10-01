package evidence

// Evidence shape ids are registered in the shape registry.
const (
	ShapeFileRegion      = "file_region"
	ShapeURL             = "url"
	ShapeCommand         = "command"
	ShapeArtifact        = "artifact"
	ShapeOpaque          = "opaque"
	ShapeStructuredEvent = "structured_event"
	ShapeVisual          = "visual"           // authored intent (render_view); never observed runtime
	ShapeSurfaceSnapshot = "surface_snapshot" // observed runtime; variants via Surface discriminator
	ShapePageGeometry    = "page_geometry"    // quantitative layout/style probe (measure_page); text only
)

// SurfaceSnapshotSurface* identify observed surface variants.
const (
	SurfaceDOM  = "dom"
	SurfaceTUI  = "tui"
	SurfaceHTTP = "http"
)

// SurfaceSnapshotHandleField* identify snapshot metadata.
const (
	SurfaceSnapshotHandleFieldSurface    = "surface"
	SurfaceSnapshotHandleFieldArtifactID = "artifact_id"
	SurfaceSnapshotHandleFieldFrameIndex = "frame_index"
)

// Trust tiers — open strings; assignment and surfacing are separate concerns.
const (
	FidelityStructured = "structured"
	FidelityScraped    = "scraped"
	FidelityOpaque     = "opaque"
)

// EvidenceMinMeaningfulSpan excludes trivial excerpts.
const EvidenceMinMeaningfulSpan = 8

// EvidenceLineBindWindow allows small line-number drift.
const EvidenceLineBindWindow = 2

// Verbatim Body capture cap; records beyond this are marked truncated.
const EvidenceCaptureBodyCapBytes = 256 * 1024

// VerifyReasonTruncated is returned when an excerpt falls beyond a truncated capture window.
const VerifyReasonTruncated = "unverified_due_to_truncation"

// DefaultUndeclaredMCPShape is the zero-config shape for undeclared MCP tool results.
const DefaultUndeclaredMCPShape = ShapeOpaque

// Claim is the typed citation checked against a ledger record.
type Claim struct {
	Path    string
	Line    int
	Excerpt string
	URL     string
	Handle  string
}

// Verifier checks a typed citation against the evidence record a handle resolves to.
type Verifier interface {
	Verify(rec Record, claim Claim) (ok bool, reason string)
}

// Normalizer canonicalizes citation tokens for a shape (OS-aware where applicable).
// Shared by ledger extraction and the leak detector.
type Normalizer interface {
	Normalize(token string) string
}

// Shape binds a shape id to its verification rule and canonicalizer.
type Shape struct {
	ID         string
	Verifier   Verifier
	Normalizer Normalizer
}

// ShapeIDs returns registered shape ids.
func ShapeIDs() []string {
	return []string{
		ShapeFileRegion,
		ShapeURL,
		ShapeCommand,
		ShapeArtifact,
		ShapeOpaque,
		ShapeStructuredEvent,
		ShapeVisual,
		ShapeSurfaceSnapshot,
		ShapePageGeometry,
	}
}

// SurfaceSnapshotHandleFields returns snapshot metadata fields.
func SurfaceSnapshotHandleFields() []string {
	return []string{
		SurfaceSnapshotHandleFieldSurface,
		SurfaceSnapshotHandleFieldArtifactID,
		SurfaceSnapshotHandleFieldFrameIndex,
	}
}

// SurfaceSnapshotSurfaces returns observed surface discriminators.
func SurfaceSnapshotSurfaces() []string {
	return []string{SurfaceDOM, SurfaceTUI, SurfaceHTTP}
}

// EvidenceVerbatimShapeIDs are classification-free verbatim-substring shapes.
func EvidenceVerbatimShapeIDs() []string {
	return []string{ShapeCommand, ShapeOpaque, ShapeStructuredEvent}
}

// EvidenceTrustTierIDs returns the locked trust-tier set.
func EvidenceTrustTierIDs() []string {
	return []string{
		FidelityStructured,
		FidelityScraped,
		FidelityOpaque,
	}
}
