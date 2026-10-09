package guidance

import (
	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ErrGroundingEscalated is returned when circuit breaker blocks coordinator prompts.
var ErrGroundingEscalated error = noticeerr.NewSentinel("grounding_escalated", wire.NoticeCodeGroundingEscalated)
