package commandprogram

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// OfferedOperations is frozen for one invocation. Discoverability or registry
// presence never substitutes for the offered capability selected by the host.
type OfferedOperations struct {
	digests       map[string]string
	catalogDigest string
}

func Offer(definitions ...toolcontract.OperationDefinition) OfferedOperations {
	digests := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		digests[definition.Facts().Identity] = definition.Digest()
	}
	return OfferedOperations{digests: digests, catalogDigest: toolcontract.LanguageCatalogDigest()}
}

// Binding calls one complete typed operation for either interface. The adapter
// supplies the owner's admission and implementation together; it must not pass
// a model handler or parse a structured tool response back into domain facts.
type Binding[Input, Result any] struct {
	Definition  toolcontract.OperationDefinition
	InvokeOwner func(context.Context, toolcontract.OperationInvocation, Input) (Result, error)
}

func Invoke[Input, Result any](ctx context.Context, offered OfferedOperations, binding Binding[Input, Result], invocation toolcontract.OperationInvocation, input Input) (Result, error) {
	var zero Result
	if err := offered.Validate(binding.Definition, invocation); err != nil {
		return zero, err
	}
	facts := binding.Definition.Facts()

	if binding.InvokeOwner == nil {
		return zero, fmt.Errorf("operation %s has no runtime binding", facts.Identity)
	}
	return binding.InvokeOwner(ctx, invocation, input)
}

// Validate checks frozen exposure and invocation identity without invoking an
// owner. Effect admission uses this same check before any authority request.
func (offered OfferedOperations) Validate(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation) error {
	facts := definition.Facts()
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
