package llm

import (
	"fmt"
)

// ModelCatalogUnavailableError means discovery could not establish an assignment.
type ModelCatalogUnavailableError struct {
	ProviderID string
	Model      string
	Cause      error
}

func (e *ModelCatalogUnavailableError) Error() string {
	return fmt.Sprintf("cannot validate model %q for provider %q: model discovery failed: %v", e.Model, e.ProviderID, e.Cause)
}

func (e *ModelCatalogUnavailableError) Unwrap() error { return e.Cause }
