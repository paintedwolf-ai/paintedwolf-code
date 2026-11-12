package spawn

import (
	"math"
	"time"
)

// RoundPolicy bounds a worker round's tool-less responses and sets when its
// coordinator is told the round is long or quiet. Notices inform; none stops
// the round.
type RoundPolicy struct {
	// ToollessResponseBound is how many consecutive responses without a tool
	// call end the round as failed with reason report_missing.
	ToollessResponseBound int
	// ConsumptionNoticeModelCalls is the model-call count of the first
	// consumption notice; later notices follow at each doubling.
	ConsumptionNoticeModelCalls int
	// QuietNoticeAfter is the time without durable progress before the first
	// quiet notice; later notices follow at each doubling.
	QuietNoticeAfter time.Duration
}

// DefaultRoundPolicy is the policy a host without workers config uses.
func DefaultRoundPolicy() RoundPolicy {
	return RoundPolicy{ToollessResponseBound: 3, ConsumptionNoticeModelCalls: 40, QuietNoticeAfter: 10 * time.Minute}
}

// ThresholdCrossed reports whether a measurement that moved from prev to cur
// passed base or one of its doublings, so each threshold fires once.
func ThresholdCrossed(prev, cur, base int64) bool {
	if base <= 0 || cur < base || cur <= prev {
		return false
	}
	for threshold := base; threshold <= cur; threshold *= 2 {
		if threshold > prev {
			return true
		}
		if threshold > math.MaxInt64/2 {
			return false
		}
	}
	return false
}
