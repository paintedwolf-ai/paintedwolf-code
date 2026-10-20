package toolcontract

import "fmt"

// ExecutionIdentity separates one static node from each dynamic invocation.
// Retrying a held execution retains this identity; a new attempt allocates another.
// These are host identities, never synthetic model tool-call identifiers.
type ExecutionIdentity struct {
	ProgramID         string
	NodeID            string
	ExecutionID       string
	ParentExecutionID string
}

// OperationScope is the explicitly addressed project tree, not ambient cwd.
type OperationScope struct {
	ProjectID string
	RootID    string
	BranchID  string
	Directory string
}

// OperationInvocation contains immutable admission inputs, not live capabilities.
// ActorID identifies the person/agent principal through existing people authority.
type OperationInvocation struct {
	Identity      ExecutionIdentity
	ActorID       string
	SessionID     string
	Scope         OperationScope
	SourceUnitID  string
	Span          SourceSpan
	CatalogDigest string
}

func (in OperationInvocation) Validate() error {
	if in.Identity.ProgramID == "" || in.Identity.NodeID == "" || in.Identity.ExecutionID == "" || in.ActorID == "" || in.SessionID == "" || in.Scope.ProjectID == "" || in.SourceUnitID == "" || in.CatalogDigest == "" {
		return fmt.Errorf("operation invocation requires explicit identity, actor, session, scope, source and catalog")
	}
	if in.Span.StartByte < 0 || in.Span.EndByte < in.Span.StartByte || in.Span.Line < 1 || in.Span.Column < 1 {
		return fmt.Errorf("invalid operation source span")
	}
	return nil
}

// SourcePin binds computation to an exact captured representation. It does not
// establish a conversational read base; only a delivered mapping can do that.
type SourcePin struct {
	SourceID       string
	Revision       string
	Representation SourceRepresentation
	SHA256         string
	StartByte      int64
	EndByte        int64
}

// SourceRepresentation describes captured bytes, not the digest of another
// representation. SHA256 always hashes the exact captured byte sequence.
// Source scripts accept only UTF8SourceBytes; their spans index those bytes.
type SourceRepresentation string

const (
	UTF8SourceBytes      SourceRepresentation = "utf8-no-bom"
	LogicalUTF8LF        SourceRepresentation = "logical-utf8-lf"
	OriginalEncodedBytes SourceRepresentation = "original-encoded-bytes"
)

// DeliveryMapping ties exact source ranges to bytes actually delivered in a response.
type DeliveryMapping struct {
	ResponseID      string
	DeliveryID      string
	Source          SourcePin
	OutputStartByte int64
	OutputEndByte   int64
}

// ProducerResult keeps utility status separate from stream completeness and
// publication eligibility. grep/diff status one can still produce complete data.
type ProducerResult struct {
	Status              int
	Terminal            ProducerTerminal
	StopCode            string
	Complete            bool
	PublicationEligible bool
	EffectCertainty     EffectCertainty
	Evidence            OperationEvidence
}

type EffectCertainty string

const (
	NoEffect        EffectCertainty = "none"
	EffectCompleted EffectCertainty = "completed"
	EffectAttempted EffectCertainty = "attempted"
	EffectUnknown   EffectCertainty = "unknown"
)

// OperationEvidence identifies the invoked domain's authoritative terminal and
// recovery facts. The aggregate records these without settling them again.
type OperationEvidence struct {
	Owner         string
	OwnerRef      string
	Kind          string
	EvidenceRef   string
	RecoveryRef   string
	HeldVersionID string
}

type ProducerTerminal string

const (
	ProducerCompleted   ProducerTerminal = "completed"
	ProducerRefused     ProducerTerminal = "refused"
	ProducerInterrupted ProducerTerminal = "interrupted"
	ProducerFailed      ProducerTerminal = "failed"
)

func (r ProducerResult) Validate() error {
	if r.Status < 0 || r.Status > 255 {
		return fmt.Errorf("invalid utility status")
	}
	switch r.Terminal {
	case ProducerCompleted, ProducerRefused, ProducerInterrupted, ProducerFailed:
	default:
		return fmt.Errorf("invalid producer terminal outcome")
	}
	if r.PublicationEligible && (!r.Complete || r.Terminal != ProducerCompleted || r.StopCode != "") {
		return fmt.Errorf("incomplete or stopped producer is not publication eligible")
	}
	return nil
}

// SessionResource identifies an owned job or listener without inventing an OS PID.
type SessionResource struct {
	SessionID   string
	ExecutionID string
	ResourceID  string
	Kind        string
}
