package guidance

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
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

var (
	fallbackGuidanceOnce     sync.Once
	fallbackGuidanceRenderer *prompts.GuidanceRenderer
	plainCommandOperandRe    = regexp.MustCompile(`^[A-Za-z0-9_./:-]+$`)
)

// QuoteCommandPath formats path as one command-language-v1 operand while
// keeping leading @label/ session addresses unquoted so they resolve.
func QuoteCommandPath(path string) string {
	if area, rel, ok := projectroot.SessionAddress(path); ok {
		if rel == "" {
			return "@" + area
		}
		if plainCommandOperandRe.MatchString(rel) {
			return "@" + area + "/" + rel
		}
		return "@" + area + "/'" + strings.ReplaceAll(rel, "'", `'\''`) + "'"
	}
	if path != "" && !strings.HasPrefix(path, "-") && !strings.HasPrefix(path, "@") && plainCommandOperandRe.MatchString(path) {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// RenderCommandOutputOmitted renders the bounded command output notice against the turn's frozen reach.
func RenderCommandOutputOmitted(ctx context.Context, reach toolreach.Request, omittedLines, omittedSize, outputPath string, nextLine, endLine, totalLines int, totalBytes int64, complete bool) (string, error) {
	data := map[string]any{
		"omitted_lines":      omittedLines,
		"complete":           complete,
		"total_lines":        totalLines,
		"total_bytes":        totalBytes,
		"omitted_size":       omittedSize,
		"output_path":        outputPath,
		"quoted_output_path": QuoteCommandPath(outputPath),
		"next_line":          nextLine,
		"end_line":           endLine,
	}
	block, err := RenderGuidanceWithReach(ctx, reach, UtilityCommandOutputOmittedRef, data)
	if err == nil && block != "" {
		return block, nil
	}
	if !reach.Known() {
		return "", fmt.Errorf("command-output-omitted requires a known toolreach.Request")
	}
	fallbackGuidanceOnce.Do(func() {
		fallbackGuidanceRenderer = prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	})
	raw, renderErr := fallbackGuidanceRenderer.RenderWithReach(ctx, reach, UtilityCommandOutputOmittedRef, data)
	if renderErr != nil {
		if err != nil {
			return "", err
		}
		return "", renderErr
	}
	return strings.TrimSpace(raw), nil
}

// RenderTodayLine renders the date-awareness preamble for lite-model calls.
func RenderTodayLine(ctx context.Context, now time.Time, cutoff string) (string, error) {
	return prompts.RenderUtilityPrompt(ctx, UtilityTodayLineRef, map[string]any{
		"weekday": now.Format("Monday"),
		"date":    now.Format("2006-01-02"),
		"cutoff":  strings.TrimSpace(cutoff),
	})
}
