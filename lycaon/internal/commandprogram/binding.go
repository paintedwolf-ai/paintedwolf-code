package commandprogram

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// OfferedOperations records the definitions selected for an invocation.
type OfferedOperations struct {
	digests       map[string]string
	catalogDigest string
}

func Offer(definitions ...toolcontract.OperationDefinition) OfferedOperations {
	digests := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		digests[definition.ReadOnlyFacts().Identity] = definition.Digest()
	}
	return OfferedOperations{digests: digests, catalogDigest: toolcontract.LanguageCatalogDigest()}
}

// Validate checks the selected definition and invocation before effect admission.
func (offered OfferedOperations) Validate(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation) error {
	facts := definition.ReadOnlyFacts()
	if definition.Digest() == "" || offered.digests[facts.Identity] != definition.Digest() {
		return &AdmissionError{Code: "TOOL_NOT_OFFERED", Operation: facts.Identity}
	}
	if err := invocation.Validate(); err != nil {
		return err
	}
	if invocation.CatalogDigest != offered.catalogDigest || facts.CatalogDigest != offered.catalogDigest {
		return fmt.Errorf("invocation and offered operation catalog mismatch")
	}
	return nil
}

// AdmissionError carries an existing public rejection meaning without prose inference.
type AdmissionError struct{ Code, Operation string }

func (e *AdmissionError) Error() string { return e.Code + ": " + e.Operation }

// NextExecution allocates a dynamic child while retaining its static node.
// Held resumption keeps the returned value rather than calling this again.
func NextExecution(parent toolcontract.OperationInvocation, nodeID string, span toolcontract.SourceSpan) toolcontract.OperationInvocation {
	parent.Identity = toolcontract.ExecutionIdentity{ProgramID: parent.Identity.ProgramID, NodeID: nodeID,
		ExecutionID: uuid.NewString(), ParentExecutionID: parent.Identity.ExecutionID}
	parent.Span = span
	return parent
}
