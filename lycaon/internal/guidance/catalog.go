package guidance

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	UtilityPickSeedsSystemRef     = "utility/pick-seeds-system"
	UtilityPickSeedsUserRef       = "utility/pick-seeds-user"
	UtilityRationaleSystemRef     = "utility/rationale-system"
	UtilityRationaleUserRef       = "utility/rationale-user"
	UtilityVerifyDetectSystemRef  = "utility/verify-detect-system"
	UtilityCommitMessageSystemRef = "utility/commit-message-system"
	UtilitySessionTitleSystemRef  = "utility/session-title-system"
	UtilityProjectNameSystemRef   = "utility/project-name-system"
	UtilityTodayLineRef           = "utility/today-line"
	SurveyGrepSpecificsRef        = "survey/grep-specifics"
	SurveyFindSpecificsRef        = "survey/find-specifics"
	SurveyReadSpecificsRef        = "survey/read-specifics"
	SurveyJQSpecificsRef          = "survey/jq-specifics"
	SurveyJQZoomRef               = "survey/jq-zoom"
	SurveyGrepEmptyTextRef        = "survey/grep-empty-text"
	SurveyGrepEmptyStructuralRef  = "survey/grep-empty-structural"
	SurveyGrepScopeDenseRef       = "survey/grep-scope-dense"
)

// RenderCatalog renders a guidance template and fails closed on empty output.
func RenderCatalog(ctx context.Context, ref string, data map[string]any) (string, error) {
	block, err := RenderGuidance(ctx, ref, data)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("guidance %q rendered empty", ref)
	}
	return block, nil
}

// RenderTodayLine renders the date-awareness preamble for lite-model calls.
func RenderTodayLine(ctx context.Context, now time.Time, cutoff string) (string, error) {
	return RenderCatalog(ctx, UtilityTodayLineRef, map[string]any{
		"weekday": now.Format("Monday"),
		"date":    now.Format("2006-01-02"),
		"cutoff":  strings.TrimSpace(cutoff),
	})
}
