package runstate

import (
	"strconv"
	"strings"
)

func ReviewLoopAttemptPath(phaseID string) string {
	return "review_loop." + phaseID + ".attempt"
}

func ReviewLoopAttempt(vars map[string]any, phaseID string) int {
	if s, ok := DotPathString(vars, ReviewLoopAttemptPath(phaseID)); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return 0
}

func BumpReviewLoopAttempt(vars map[string]any, phaseID string) map[string]any {
	return SetHostVar(vars, ReviewLoopAttemptPath(phaseID), strconv.Itoa(ReviewLoopAttempt(vars, phaseID)+1))
}
