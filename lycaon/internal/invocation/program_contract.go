package invocation

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// validateChildStart adds the receipt trace ceiling to the structural contract.
func validateChildStart(c toolcontract.ProgramChildStart) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return validateReviewedInputs(c.ExpandedInputs)
}

// RetainedOutput identifies the actual byte-retention claim separately from
// operation provenance. A spill's initiating operation is not its claim marker.
type RetainedOutput struct {
	ExecutionID      string
	Stream           string
	Reference        string
	RetentionClaimID string
	Complete         bool
	Redacted         bool
	Mappings         []toolcontract.StreamSourceMapping
}
