package inputs

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestNormalizeSecretAskRejectsUnusableLifecycleMetadataBeforePrompting(t *testing.T) {
	for name, secret := range map[string]*workflowdef.SecretInputSpec{
		"project without purpose": {Name: "Deploy key", Scope: "project"},
		"lifetime beyond a year":  {Name: "Deploy key", Scope: "task", AgentUseTTLSeconds: maxAskSecretAgentUseLifetimeSeconds + 1},
		"name too long":           {Name: strings.Repeat("x", 81), Scope: "task"},
		"purpose too long":        {Name: "Deploy key", Scope: "task", Purpose: strings.Repeat("x", 241)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeAskUserRequest(UserInputRequest{
				Prompt: "Provide it", ResponseType: workflowdef.FeedbackResponseSecret, Secret: secret,
			})
			reject := &AskUserReject{}
			if !errors.As(err, &reject) ||
				(reject.Code != "ASK_USER_SECRET_METADATA_REQUIRED" && reject.Code != "ASK_USER_SECRET_METADATA_INVALID") {
				t.Fatalf("error = %v", err)
			}
			if len(reject.Data) != 1 || reject.Data["field"] == "" {
				t.Fatalf("metadata refusal must identify only the invalid field: %+v", reject.Data)
			}
			for key := range reject.Data {
				if key != "field" {
					t.Fatalf("protected metadata entered feedback at %s", key)
				}
			}
		})
	}
}
func TestAskInputNeverDowngradesProtectedChannel(t *testing.T) {
	for _, refs := range [][]string{nil, {"one"}, {"one", "two"}} {
		for _, responseType := range []workflowdef.FeedbackResponseType{workflowdef.FeedbackResponseSecret, workflowdef.FeedbackResponseText, "invalid"} {
			req := UserInputRequest{Prompt: "Provide input", ResponseType: responseType, Artifacts: refs, Secret: &workflowdef.SecretInputSpec{Name: "Key"}}
			norm, err := normalizeAskUserRequest(req)
			if responseType == workflowdef.FeedbackResponseSecret && len(refs) == 0 {
				testutil.FailErr(t, "normalize protected request", err)
				if norm.rt != workflowdef.FeedbackResponseSecret || norm.secret == nil || norm.purpose != "secret" {
					t.Fatalf("protected request downgraded: %+v", norm)
				}
				continue
			}
			var reject *AskUserReject
			if !errors.As(err, &reject) {
				t.Fatalf("incompatible request created a card: %+v", norm)
			}
			want := "ASK_USER_SECRET_METADATA_FORBIDDEN"
			if responseType == workflowdef.FeedbackResponseSecret {
				want = "ASK_USER_SECRET_ARTIFACTS_FORBIDDEN"
			}
			if responseType == "invalid" {
				want = "ASK_USER_RESPONSE_TYPE_INVALID"
			}
			if string(reject.Code) != want {
				t.Fatalf("type=%s artifacts=%d code=%s want=%s", responseType, len(refs), reject.Code, want)
			}
		}
	}
}
