package llm

import (
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

const defaultUtilityCallTimeout = 2 * time.Minute

// utilityBudgetExceededError labels an attempt the utility budget ended, so
// logs tell it apart from a caller's own deadline.
type utilityBudgetExceededError struct{ cause error }

func (e *utilityBudgetExceededError) Error() string {
	return fmt.Sprintf("utility call budget exceeded: %v", e.cause)
}

func (e *utilityBudgetExceededError) Unwrap() error { return e.cause }

func resolveUtilityCallTimeout(profile providerprofile.Profile, class UtilityClass) time.Duration {
	if class == UtilityClassBackground && profile.BackgroundUtilityCallTimeout > 0 {
		return profile.BackgroundUtilityCallTimeout
	}
	if profile.UtilityCallTimeout > 0 {
		return profile.UtilityCallTimeout
	}
	return defaultUtilityCallTimeout
}
