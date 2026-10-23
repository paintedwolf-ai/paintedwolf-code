package guidance

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
)

const (
	UtilityPickSeedsSystemRef      = "utility/pick-seeds-system"
	UtilityPickSeedsUserRef        = "utility/pick-seeds-user"
	UtilityRationaleSystemRef      = "utility/rationale-system"
	UtilityRationaleUserRef        = "utility/rationale-user"
	UtilityVerifyDetectSystemRef   = "utility/verify-detect-system"
	UtilityCommitMessageSystemRef  = "utility/commit-message-system"
	UtilitySessionTitleSystemRef   = "utility/session-title-system"
	UtilityProjectNameSystemRef    = "utility/project-name-system"
	UtilityTodayLineRef            = "utility/today-line"
	UtilityCommandOutputOmittedRef = "utility/command-output-omitted"
	SurveyGrepSpecificsRef         = "survey/grep-specifics"
	SurveyFindSpecificsRef         = "survey/find-specifics"
	SurveyReadSpecificsRef         = "survey/read-specifics"
	SurveyJQSpecificsRef           = "survey/jq-specifics"
	SurveyJQZoomRef                = "survey/jq-zoom"
	SurveyGrepEmptyTextRef         = "survey/grep-empty-text"
	SurveyGrepEmptyStructuralRef   = "survey/grep-empty-structural"
	SurveyGrepScopeDenseRef        = "survey/grep-scope-dense"
	SurveyCommandUnsavedDraftRef   = "survey/command-unsaved-draft"
)

var (
	fallbackGuidanceOnce     sync.Once
	fallbackGuidanceRenderer *prompts.GuidanceRenderer
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

// RenderCommandOutputOmitted renders the terminal-style omission notice for bounded command output.
func RenderCommandOutputOmitted(ctx context.Context, omittedLines, omittedSize, outputPath string, nextLine, endLine, totalLines int, totalBytes int64, complete bool) (string, error) {
	data := map[string]any{
		"omitted_lines": omittedLines,
		"complete":      complete,
		"total_lines":   totalLines, "total_bytes": totalBytes,
		"omitted_size": omittedSize,
		"output_path":  outputPath,
		"next_line":    nextLine,
		"end_line":     endLine,
	}
	block, err := RenderCatalog(ctx, UtilityCommandOutputOmittedRef, data)
	if err == nil {
		return block, nil
	}
	fallbackGuidanceOnce.Do(func() {
		fallbackGuidanceRenderer = prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	})
	raw, renderErr := fallbackGuidanceRenderer.Render(ctx, UtilityCommandOutputOmittedRef, data)
	if renderErr != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

// RenderTodayLine renders the date-awareness preamble for lite-model calls.
func RenderTodayLine(ctx context.Context, now time.Time, cutoff string) (string, error) {
	return RenderCatalog(ctx, UtilityTodayLineRef, map[string]any{
		"weekday": now.Format("Monday"),
		"date":    now.Format("2006-01-02"),
		"cutoff":  strings.TrimSpace(cutoff),
	})
}
