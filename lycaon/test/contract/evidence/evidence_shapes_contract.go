package contract

import "github.com/lycaon/lycaon/internal/evidence"

// expectedEvidenceKindShapes mirrors the bundled evidence catalog.
var expectedEvidenceKindShapes = map[string]string{
	"read":             evidence.ShapeFileRegion,
	"grep":             evidence.ShapeFileRegion,
	"find":             evidence.ShapeFileRegion,
	"list":             evidence.ShapeFileRegion,
	"stat":             evidence.ShapeFileRegion,
	"wc":               evidence.ShapeFileRegion,
	"edit":             evidence.ShapeFileRegion,
	"write":            evidence.ShapeFileRegion,
	"replace":          evidence.ShapeFileRegion,
	"restore":          evidence.ShapeFileRegion,
	"rewrite":          evidence.ShapeFileRegion,
	"delete":           evidence.ShapeFileRegion,
	"diff":             evidence.ShapeFileRegion,
	"web":              evidence.ShapeURL,
	"command":          evidence.ShapeCommand,
	"git_receipt":      evidence.ShapeCommand,
	"git":              evidence.ShapeCommand,
	"scan":             evidence.ShapeArtifact,
	"survey":           evidence.ShapeFileRegion,
	"summarize":        evidence.ShapeFileRegion,
	"recall":           evidence.ShapeFileRegion,
	"provenance":       evidence.ShapeFileRegion,
	"skill":            evidence.ShapeArtifact,
	"render":           evidence.ShapeVisual,
	"page":             evidence.ShapeSurfaceSnapshot,
	"tui":              evidence.ShapeSurfaceSnapshot,
	"page_geometry":    evidence.ShapePageGeometry,
	"secret_lifecycle": evidence.ShapeStructuredEvent,
	"http_response":    evidence.ShapeSurfaceSnapshot,
	"terminal_session": evidence.ShapeSurfaceSnapshot,
}

var evidenceSurveyKinds = []string{
	"read", "grep", "find", "list", "stat", "wc", "git", "survey", "recall", "provenance",
}

var evidenceMutationKinds = []string{
	"edit", "write", "replace", "restore", "rewrite", "delete",
}
