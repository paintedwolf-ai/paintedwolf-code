package guidance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
)

const (
	UtilityPickSeedsSystemRef      = "utility/pick-seeds-system"
	UtilityPickSeedsUserRef        = "utility/pick-seeds-user"
	UtilityRationaleSystemRef      = "utility/rationale-system"
	UtilityRationaleUserRef        = "utility/rationale-user"
	UtilityActivityChangeSystemRef = "utility/activity-change-system"
	UtilityActivityChangeUserRef   = "utility/activity-change-user"
	UtilityWorkerResultSystemRef   = "utility/worker-result-system"
	UtilityWorkerResultUserRef     = "utility/worker-result-user"
	UtilityStepChangeSystemRef     = "utility/step-change-system"
	UtilityStepChangeUserRef       = "utility/step-change-user"
	UtilityVerifyDetectSystemRef   = "utility/verify-detect-system"
	UtilityCommitMessageSystemRef  = "utility/commit-message-system"
	UtilitySessionTitleSystemRef   = "utility/session-title-system"
	UtilityProjectNameSystemRef    = "utility/project-name-system"
	UtilityTodayLineRef            = "utility/today-line"
	UtilityCommandOutputOmittedRef = "utility/command-output-omitted"
	SurveyJQZoomRef                = "survey/jq-zoom"
	SurveyGrepEmptyTextRef         = "survey/grep-empty-text"
	SurveyGrepEmptyStructuralRef   = "survey/grep-empty-structural"
	SurveyGrepScopeDenseRef        = "survey/grep-scope-dense"
)

// RenderCommandOutputOmitted renders the bounded command output notice against the turn's frozen reach.
func RenderCommandOutputOmitted(ctx context.Context, reach toolreach.Request, omittedLines, omittedSize, outputPath string, nextLine, endLine, totalLines int, totalBytes int64, complete bool) (string, error) {
	data := map[string]any{
		"omitted_lines":      omittedLines,
		"complete":           complete,
		"total_lines":        totalLines,
		"total_bytes":        totalBytes,
		"omitted_size":       omittedSize,
		"output_path":        outputPath,
		"quoted_output_path": toolreach.QuoteProgram([]string{outputPath}),
		"next_line":          nextLine,
		"end_line":           endLine,
	}
	block, err := RenderGuidanceWithReach(ctx, reach, UtilityCommandOutputOmittedRef, data)
	if err != nil {
		return "", err
	}
	if block == "" {
		return "", fmt.Errorf("%s rendered no notice", UtilityCommandOutputOmittedRef)
	}
	return block, nil
}

// RenderTodayLine renders the date-awareness preamble for lite-model calls.
func RenderTodayLine(ctx context.Context, now time.Time, cutoff string) (string, error) {
	return prompts.RenderUtilityPrompt(ctx, UtilityTodayLineRef, map[string]any{
		"weekday": now.Format("Monday"),
		"date":    now.Format("2006-01-02"),
		"cutoff":  strings.TrimSpace(cutoff),
	})
}
