package turnload

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/decide"
)

// RankingFailure identifies why selection could not complete.
type RankingFailure string

const (
	RankingUnavailable RankingFailure = "unavailable"
	RankingDisabled    RankingFailure = "disabled"
	RankingTimeout     RankingFailure = "timeout"
	RankingCanceled    RankingFailure = "canceled"
	RankingFault       RankingFailure = "fault"
)

func rankingFailure(err error) RankingFailure {
	switch {
	case errors.Is(err, decide.ErrUnavailable):
		return RankingUnavailable
	case errors.Is(err, decide.ErrDisabled):
		return RankingDisabled
	case errors.Is(err, decide.ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		return RankingTimeout
	case errors.Is(err, context.Canceled):
		return RankingCanceled
	default:
		return RankingFault
	}
}

func validateRanking(scores []float64, count int) error {
	if len(scores) != count {
		return fmt.Errorf("ranking returned %d scores for %d candidates", len(scores), count)
	}
	for _, score := range scores {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > RankScoreCeiling {
			return fmt.Errorf("ranking returned an invalid score")
		}
	}
	return nil
}
