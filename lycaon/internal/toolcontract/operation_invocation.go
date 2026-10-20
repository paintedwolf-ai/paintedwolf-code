package toolcontract

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

// SourcePin binds computation to an exact captured representation. It does not
// establish a conversational read base; only a delivered mapping can do that.
type SourcePin struct {
	SourceID       string
	Revision       string
	Representation string
	SHA256         string
	StartByte      int64
	EndByte        int64
}

// DeliveryMapping ties exact source ranges to bytes actually delivered in a response.
type DeliveryMapping struct {
	ResponseID string
	DeliveryID string
	Source     SourcePin
	OutputStartByte int64
	OutputEndByte   int64
}

// ProducerResult keeps utility status separate from stream completeness and
// publication eligibility. grep/diff status one can still produce complete data.
type ProducerResult struct {
	Status              int
	Terminal            string
	StopCode            string
	Complete            bool
	PublicationEligible bool
	EffectCertainty     string
}

// SessionResource identifies an owned job or listener without inventing an OS PID.
type SessionResource struct {
	SessionID   string
	ExecutionID string
	ResourceID  string
	Kind        string
}
