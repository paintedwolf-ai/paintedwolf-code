package search

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	SourceMessage      = "message"
	SourceTool         = "tool"
	SourceFinding      = "finding"
	SourceArtifact     = "artifact"
	SourceGateEvidence = "gate"
)

const (
	HitKindEvidence = "evidence"
	HitKindMessage  = "message"
	HitKindClaim    = "claim"
	HitKindFinding  = "finding"
	HitKindWeb      = "web"
	HitKindCode     = "code"
	HitKindFile     = "file"   // a file whose path matched — a navigation target, not a content match
	HitKindSymbol   = "symbol" // a declaration whose name matched — a navigation target
	HitKindTool     = "tool"
	HitKindNetwork  = "network" // a host a confined command reached (egress observation)
	HitKindArtifact = "artifact"
	HitKindOutcome  = "outcome"
)

const SourceCode = "code"

// IndexRow is one projected observation in evidence_index.
type IndexRow struct {
	ID            string
	ProjectID     string
	Source        string
	HitKind       string
	SessionID     string
	RootSessionID string
	MessageID     string
	SourceRef     string
	LegID         string
	WorkflowRunID string
	Handle        string
	// Tool names the producing tool on tool-call and tool-result rows.
	Tool      string
	Kind      string
	Shape     string
	Role      string
	Path      string
	Line      int
	URL       string
	Snippet   string
	Verified  *bool
	HintCode  string
	CheckID   string
	Trust     string
	Verdict   string
	Truncated bool
	Untrusted bool
	TS        string
}

// RowID returns a deterministic evidence_index primary key.
func RowID(source, sourceRef, suffix string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + sourceRef + "\x00" + suffix))
	return hex.EncodeToString(sum[:16])
}
