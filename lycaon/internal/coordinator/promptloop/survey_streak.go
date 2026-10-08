package promptloop

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// SurveyStreakBatches sets the interval between read-only streak notices.
const SurveyStreakBatches = 6

// SurveyStreakNudge renders the inform for a read-only batch streak.
type SurveyStreakNudge func(ctx context.Context, sess *api.Session, batches int, tools []string) HostNudge

type batchSurvey int

const (
	// Actions and unknown tools end the streak.
	batchActed batchSurvey = iota
	// Empty and bookkeeping-only batches preserve the streak.
	batchNeutral
	// Reads mixed with bookkeeping count as one read-only batch.
	batchReadOnly
)

type statusCursorInfo struct {
	valid      bool
	offset     int
	nextOffset *int
	paths      []string
	groupDepth int
}

func parseStatusCursor(res *api.ToolResult) statusCursorInfo {
	if res == nil {
		return statusCursorInfo{}
	}
	info := statusCursorInfo{valid: true}
	if res.ToolArgs != nil {
		if off, ok := res.ToolArgs["offset"].(float64); ok {
			info.offset = int(off)
		} else if off, ok := res.ToolArgs["offset"].(int); ok {
			info.offset = off
		}
		if depth, ok := res.ToolArgs["group_depth"].(float64); ok {
			info.groupDepth = int(depth)
		} else if depth, ok := res.ToolArgs["group_depth"].(int); ok {
			info.groupDepth = depth
		}
		if pSlice, ok := res.ToolArgs["paths"].([]any); ok {
			for _, p := range pSlice {
				if s, ok := p.(string); ok {
					info.paths = append(info.paths, s)
				}
			}
		} else if pSlice, ok := res.ToolArgs["paths"].([]string); ok {
			info.paths = append(info.paths, pSlice...)
		}
	}
	var resp struct {
		NextOffset *int `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(res.Content), &resp); err == nil {
		info.nextOffset = resp.NextOffset
	}
	return info
}

// Rejected and errored calls do not affect the survey streak.
func judgeBatch(reg tools.ToolRegistry, history []api.Message, assistantMessageID string) (batchSurvey, []string, statusCursorInfo) {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return batchActed, nil, statusCursorInfo{}
	}
	var names []string
	sawRead := false
	var statusResult *api.ToolResult
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.AssistantMessageID) != assistantMessageID {
			continue
		}
		switch msg.ToolResult.Outcome {
		case api.ToolResultOutcomeRejected, api.ToolResultOutcomeError:
			continue
		case api.ToolResultOutcomeCompleted:
		}
		name := strings.TrimSpace(msg.ToolResult.Tool)
		contract, ok := registryContract(reg, name)
		switch {
		case !ok:
			return batchActed, nil, statusCursorInfo{}
		case contract.SurveyNeutral:
			continue
		case contract.Lifecycle != toolcontract.LifecycleReadOnly:
			return batchActed, nil, statusCursorInfo{}
		}
		sawRead = true
		names = append(names, name)
		if name == "git_status" {
			statusResult = msg.ToolResult
		}
	}
	if !sawRead {
		return batchNeutral, nil, statusCursorInfo{}
	}
	var cursor statusCursorInfo
	if len(names) == 1 && names[0] == "git_status" && statusResult != nil {
		cursor = parseStatusCursor(statusResult)
	}
	return batchReadOnly, names, cursor
}

func (st *promptLoopTurnState) noteBatchLifecycle(verdict batchSurvey, names []string, cursor statusCursorInfo) {
	if st == nil || verdict == batchNeutral {
		return
	}
	if verdict == batchActed {
		st.readOnlyBatches = 0
		st.readOnlyStreakTools = nil
		st.surveyStreakNudged = 0
		st.statusCursorNextOffset = nil
		st.statusCursorPaths = nil
		st.statusCursorGroupDepth = 0
		return
	}
	// Continuing an existing git_status offset does not increment read-only batches.
	if cursor.valid && st.statusCursorNextOffset != nil &&
		*st.statusCursorNextOffset == cursor.offset &&
		st.statusCursorGroupDepth == cursor.groupDepth &&
		slices.Equal(st.statusCursorPaths, cursor.paths) {
		st.statusCursorNextOffset = cursor.nextOffset
		return
	}
	if cursor.valid {
		st.statusCursorNextOffset = cursor.nextOffset
		st.statusCursorPaths = cursor.paths
		st.statusCursorGroupDepth = cursor.groupDepth
	} else {
		st.statusCursorNextOffset = nil
		st.statusCursorPaths = nil
		st.statusCursorGroupDepth = 0
	}
	st.readOnlyBatches++
	for _, name := range names {
		if !slices.Contains(st.readOnlyStreakTools, name) {
			st.readOnlyStreakTools = append(st.readOnlyStreakTools, name)
		}
	}
}

// Each threshold crossing produces at most one notice.
func (st *promptLoopTurnState) surveyStreakFires() bool {
	if st == nil || st.readOnlyBatches == 0 || st.readOnlyBatches%SurveyStreakBatches != 0 {
		return false
	}
	return st.surveyStreakNudged != st.readOnlyBatches
}

func (l turnNudges) maybeSurveyStreakNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l.PromptLoop == nil || l.Deps.SurveyStreakNudge == nil || sess == nil || !st.surveyStreakFires() {
		return history, nil
	}
	nudge := l.Deps.SurveyStreakNudge(ctx, sess, st.readOnlyBatches, st.readOnlyStreakTools)
	st.surveyStreakNudged = st.readOnlyBatches
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}
