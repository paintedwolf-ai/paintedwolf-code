package inputs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestAskUserNormalizeArgsValidation(t *testing.T) {
	cases := []struct {
		name string
		req  UserInputRequest
		code string
	}{
		{"empty prompt", UserInputRequest{Prompt: "  "}, "ASK_USER_PROMPT_REQUIRED"},
		{"prompt too long", UserInputRequest{Prompt: strings.Repeat("🚀", MaxAskUserPromptRunes+1)}, "ASK_USER_PROMPT_TOO_LONG"},
		{"options on text", UserInputRequest{Prompt: "q", Options: []string{"a", "b"}}, "ASK_USER_OPTIONS_FORBIDDEN"},
		{"choice needs options", UserInputRequest{Prompt: "q", ResponseType: workflowdef.FeedbackResponseSingleChoice}, "ASK_USER_OPTIONS_REQUIRED"},
		{"artifact missing", UserInputRequest{Prompt: "q", Artifacts: []string{"a"}}, "ASK_USER_ARTIFACT_NOT_FOUND"},
		{"compare options forbidden", UserInputRequest{Prompt: "q", Purpose: "compare", Artifacts: []string{"a", "b"}, Options: []string{"Approve", "Reject"}}, "ASK_USER_COMPARE_OPTIONS"},
		{"clarify with artifacts", UserInputRequest{Prompt: "q", Purpose: "clarify", Artifacts: []string{"a"}}, "ASK_USER_PURPOSE_INVALID"},
		{"review without artifacts", UserInputRequest{Prompt: "q", Purpose: "review"}, "ASK_USER_PURPOSE_INVALID"},
		{"unknown purpose", UserInputRequest{Prompt: "q", Purpose: "vote"}, "ASK_USER_PURPOSE_INVALID"},
		{"review options forbidden", UserInputRequest{Prompt: "q", Artifacts: []string{"a"}, Options: []string{"Approve", "Reject"}, ResponseType: workflowdef.FeedbackResponseSingleChoice}, "ASK_USER_REVIEW_OPTIONS"},
		{"review multi_choice forbidden", UserInputRequest{Prompt: "q", Artifacts: []string{"a"}, ResponseType: workflowdef.FeedbackResponseMultiChoice}, "ASK_USER_REVIEW_OPTIONS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "artifact missing" {
				_, err := normalizeAskUserRequest(UserInputRequest{Prompt: "q", Artifacts: []string{"a", "b", "c", "d", "e"}})
				reject := &AskUserReject{}
				ok := errors.As(err, &reject)
				if !ok || reject.Code != "ASK_USER_COMPARE_ARITY" {
					t.Fatalf("err = %v want ASK_USER_COMPARE_ARITY", err)
				}
				return
			}
			_, err := normalizeAskUserRequest(tc.req)
			reject := &AskUserReject{}
			ok := errors.As(err, &reject)
			if !ok || string(reject.Code) != tc.code {
				t.Fatalf("err = %v want %s", err, tc.code)
			}
		})
	}
}

func TestAskUserParseArgsArtifacts(t *testing.T) {
	req, err := parseAskUserArgs(map[string]any{
		"prompt":    "Which?",
		"artifacts": []any{"017d0ba9-2b68-4931-b1b5-124b62684e66", "52f0e958-f6b4-4f5c-9a4b-0644c65d76e7"},
	})
	testutil.FailErr(t, "parseAskUserArgs failed", err)
	if len(req.Artifacts) != 2 || req.Artifacts[0] != "017d0ba9-2b68-4931-b1b5-124b62684e66" {
		t.Fatalf("artifacts = %v", req.Artifacts)
	}
	// Several artifacts without a purpose are a review; compare is explicit.
	norm, err := normalizeAskUserRequest(req)
	testutil.FailErr(t, "normalizeAskUserRequest failed", err)
	if norm.purpose != "review" || len(norm.options) != 3 {
		t.Fatalf("norm = %+v", norm)
	}
}

func TestAskUserMultiArtifactReviewAndCompare(t *testing.T) {
	arts := []string{"017d0ba9-2b68-4931-b1b5-124b62684e66", "52f0e958-f6b4-4f5c-9a4b-0644c65d76e7", "9f8e8af1-6507-4772-b141-12648dc16e20"}

	// Text response with 3 artifacts defaults to review mode with no choice options.
	normText, err := normalizeAskUserRequest(UserInputRequest{
		Prompt:       "Look these over and provide feedback",
		ResponseType: workflowdef.FeedbackResponseText,
		Artifacts:    arts,
	})
	testutil.FailErr(t, "multi-artifact text review", err)
	if normText.purpose != "review" || normText.rt != workflowdef.FeedbackResponseText || len(normText.options) != 0 {
		t.Fatalf("normText = %+v want purpose=review, rt=text, options=0", normText)
	}

	// Explicit purpose: review with single_choice synthesizes Approve / Request changes / Reject.
	normReview, err := normalizeAskUserRequest(UserInputRequest{
		Prompt:       "Review these mockups",
		Purpose:      "review",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Artifacts:    arts,
	})
	testutil.FailErr(t, "multi-artifact choice review", err)
	if normReview.purpose != "review" || normReview.rt != workflowdef.FeedbackResponseSingleChoice || len(normReview.options) != 3 {
		t.Fatalf("normReview = %+v want purpose=review, options=3", normReview)
	}
	if normReview.options[0] != "Approve" || normReview.options[1] != "Request changes" || normReview.options[2] != "Reject" {
		t.Fatalf("review options = %v", normReview.options)
	}

	// Explicit purpose: compare synthesizes A, B, C options.
	normCompare, err := normalizeAskUserRequest(UserInputRequest{
		Prompt:    "Pick a variant",
		Purpose:   "compare",
		Artifacts: arts,
	})
	testutil.FailErr(t, "multi-artifact compare", err)
	if normCompare.purpose != "compare" || normCompare.rt != workflowdef.FeedbackResponseSingleChoice || len(normCompare.options) != 3 {
		t.Fatalf("normCompare = %+v want purpose=compare, options=3", normCompare)
	}
	if normCompare.options[0] != "A" || normCompare.options[1] != "B" || normCompare.options[2] != "C" {
		t.Fatalf("compare options = %v", normCompare.options)
	}
}

func TestAskUserOptionsAtMaxOK(t *testing.T) {
	opts := make([]string, MaxAskUserOptions)
	for i := range opts {
		opts[i] = fmt.Sprintf("option %d", i+1)
	}
	norm, err := normalizeAskUserRequest(UserInputRequest{
		Prompt:       "Pick one",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      opts,
	})
	if err != nil {
		testutil.FailErr(t, "normalize at-max options", err)
	}
	if len(norm.options) != MaxAskUserOptions {
		t.Fatalf("options = %d want %d", len(norm.options), MaxAskUserOptions)
	}
}

func TestAskUserOptionsTooManyReportsCounts(t *testing.T) {
	opts := make([]string, MaxAskUserOptions+1)
	for i := range opts {
		opts[i] = fmt.Sprintf("option %d", i+1)
	}
	for _, rt := range []workflowdef.FeedbackResponseType{workflowdef.FeedbackResponseSingleChoice, workflowdef.FeedbackResponseMultiChoice} {
		_, err := normalizeAskUserRequest(UserInputRequest{Prompt: "Pick", ResponseType: rt, Options: opts})
		reject := &AskUserReject{}
		ok := errors.As(err, &reject)
		if !ok || reject.Code != "ASK_USER_OPTIONS_TOO_MANY" {
			t.Fatalf("rt %s: err = %v want ASK_USER_OPTIONS_TOO_MANY", rt, err)
		}
		if reject.Data["max_options"] != MaxAskUserOptions {
			t.Fatalf("max_options = %v want %d", reject.Data["max_options"], MaxAskUserOptions)
		}
		if reject.Data["count"] != MaxAskUserOptions+1 {
			t.Fatalf("count = %v want %d", reject.Data["count"], MaxAskUserOptions+1)
		}
	}
}

func TestAskUserPromptAtMaxRunesOK(t *testing.T) {
	_, err := normalizeAskUserRequest(UserInputRequest{Prompt: strings.Repeat("x", MaxAskUserPromptRunes)})
	if err != nil {
		t.Fatalf("max-length prompt should pass: %v", err)
	}
	_, err = normalizeAskUserRequest(UserInputRequest{Prompt: "Pick a **stack**:\n\n- Python\n- Go"})
	if err != nil {
		t.Fatalf("markdown prompt should pass: %v", err)
	}
}

func TestAskUserPromptTooLongReportsRunes(t *testing.T) {
	_, err := normalizeAskUserRequest(UserInputRequest{Prompt: strings.Repeat("🚀", MaxAskUserPromptRunes+1)})
	reject := &AskUserReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "ASK_USER_PROMPT_TOO_LONG" {
		t.Fatalf("err = %v want ASK_USER_PROMPT_TOO_LONG", err)
	}
	if reject.Data["max_runes"] != MaxAskUserPromptRunes {
		t.Fatalf("max_runes = %v want %d", reject.Data["max_runes"], MaxAskUserPromptRunes)
	}
	if reject.Data["runes"] != MaxAskUserPromptRunes+1 {
		t.Fatalf("runes = %v want %d", reject.Data["runes"], MaxAskUserPromptRunes+1)
	}
	if reject.Data["ask_prompt_runes"] != MaxAskUserPromptRunes+1 || reject.Data["ask_prompt_limit"] != MaxAskUserPromptRunes {
		t.Fatalf("OAR copy facts lost measured Unicode budget: %+v", reject.Data)
	}
}

func TestAskUserClarifyAlwaysParks(t *testing.T) {
	norm, err := normalizeAskUserRequest(UserInputRequest{Prompt: "Optional?"})
	testutil.FailErr(t, "normalizeAskUserRequest failed", err)
	if norm.purpose != "clarify" {
		t.Fatalf("norm = %+v", norm)
	}
}

func TestAskUserReviewMode(t *testing.T) {
	norm, err := normalizeAskUserRequest(UserInputRequest{
		Prompt:    "Review",
		Artifacts: []string{"art-1"},
	})
	testutil.FailErr(t, "normalizeAskUserRequest failed", err)
	if norm.purpose != "review" || len(norm.options) != 3 {
		t.Fatalf("norm = %+v", norm)
	}
}
