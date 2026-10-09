package promptadmin

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRecoveryAdmissionDoesNotConvertButtonTextIntoNewContent(t *testing.T) {
	handler, sessions, attachments, sess := newPromptAttachmentRecoveryFixture(t)
	ctx := t.Context()
	original, _, err := handler.Sessions.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "review", promptinput.Input{Text: "Review"})
	testutil.FailErr(t, "admit original", err)
	claimed, _, err := sessions.ClaimPromptSubmission(ctx, original.ID)
	testutil.FailErr(t, "claim original", err)
	progressID := uuid.NewString()
	testutil.FailErr(t, "record progress", sessions.AppendMessages(ctx, sess.ID,
		wire.Message{ID: original.ID, Role: wire.MessageRoleUser, Content: "Review", Visibility: wire.MessageVisibilityTranscript},
		wire.Message{ID: progressID, Role: wire.MessageRoleAssistant, Content: "Reviewed", Visibility: wire.MessageVisibilityTranscript}))
	testutil.FailErr(t, "fail provider", sessions.FinishPromptSubmission(ctx, original.ID, claimed.ClaimToken, store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: "provider disconnected"}))
	req := wire.PromptRequest{OperationID: uuid.NewString(), Text: "Keep going", Recovery: &wire.PromptRecovery{Action: "continue", AfterMessageID: progressID}}
	prepared, ok := handler.preparePromptAdmission(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), sess.ID, sess, req, attachments, req.Text, req.Text, false)
	if !ok || len(prepared.input.ContentParts) != 0 {
		t.Fatalf("recovery became a new content message: %+v", prepared)
	}
	admitted, _, err := handler.Sessions.Submissions.AdmitPrompt(ctx, sess.ID, req.OperationID, req, prepared.input)
	testutil.FailErr(t, "admit button recovery", err)
	if admitted == nil {
		t.Fatal("recovery not persisted")
	}
}
