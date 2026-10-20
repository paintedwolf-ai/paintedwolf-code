package fseffect

import (
	"errors"
	"fmt"
	"testing"
)

func TestUnavailableMetadataNeverClaimsAnEffect(t *testing.T) {
	for _, cause := range []error{ErrUnsupported, fmt.Errorf("fixture capability: %w", ErrUnsupported)} {
		result := ownershipOutcome(cause)
		if result.Applied || result.Capability != MetadataUnsupported {
			t.Fatalf("unsupported outcome = %+v", result)
		}
	}
	result := ownershipOutcome(errors.New("fixture permission failure"))
	if result.Applied || result.Capability != MetadataUndetermined {
		t.Fatalf("failed outcome = %+v", result)
	}
}
