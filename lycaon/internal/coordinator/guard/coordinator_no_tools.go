package guard

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorEvidenceOptional decides whether a closeout may omit citations: true
// when the current intent window holds no citable observation, even when an
// earlier window does. Supplied references are validated either way.
func CoordinatorEvidenceOptional(history []api.Message, ev guidance.CloseoutEvidence) bool {
	return !guidance.IntentWindowHasCitableObservations(history) && !ev.LegObservations
}
