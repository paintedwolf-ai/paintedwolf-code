package tools

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func TestObservedFailureIdentitySurvivesSharedTokensAndNewTools(t *testing.T) {
	bp := stockBlockPlane(t)
	formatter := bp.Renderer.Reject.(*guidance.StaticRejectFormatter)
	for _, tool := range []string{"git_commit", "git_restore", "read", "write", "future_tool"} {
		for _, code := range []string{
			"GIT_SIGNING_UNSUPPORTED", "GIT_REPO_CONFIG_UNSAFE",
			"GIT_COMMIT_PATH_DENIED", "GIT_RESTORE_PATH_DENIED", "READ_PATH_NOT_FOUND", "FUTURE_TOOL_FAILURE",
		} {
			t.Run(tool+"/"+code, func(t *testing.T) {
				reject := &ToolReject{Code: code, Data: map[string]any{"path": "AGENTS.md", "reason": "fixture failure"}}
				if code == "FUTURE_TOOL_FAILURE" {
					reject.Observation = "policy_denied"
				}
				err := bp.RejectObservation(t.Context(), tool, "coordinator", nil, reject)
				if err == nil {
					err = RenderReject(reject, formatter)
				}
				refusal, ok := guidance.RefusalFromError(err)
				if !ok || refusal.Code() != code {
					t.Fatalf("failure %s became %v", code, err)
				}
				if AsToolReject(err) != reject {
					t.Fatalf("original typed failure was lost: %v", err)
				}
			})
		}
	}
}

type failingRejectFormatter struct{}

func (failingRejectFormatter) Format(string, map[string]any) (string, error) {
	return "", errors.New("fixture template failure")
}

func TestRejectedOperationSurvivesFormatterFailure(t *testing.T) {
	bp := stockBlockPlane(t)
	bp.Renderer.Reject = failingRejectFormatter{}
	reject := &ToolReject{Code: "GIT_SIGNING_UNSUPPORTED", Data: map[string]any{"reason": "signature unavailable"}}
	err := bp.RejectObservation(t.Context(), "git_commit", "coordinator", nil, reject)
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal.Code() != reject.Code || AsToolReject(err) != reject {
		t.Fatalf("formatter failure replaced original rejection: %v", err)
	}
}
