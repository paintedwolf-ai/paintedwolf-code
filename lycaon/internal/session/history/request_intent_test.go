package history

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestRequestRestoresInstructionMissingFromAssembledMessages(t *testing.T) {
	opener := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build a game"}
	capture := api.Message{ID: "capture", Ord: 11, Role: api.MessageRoleTool}
	history := []api.Message{opener, capture}
	req := modelcall.CompletionRequest{Messages: []api.Message{{ID: "system", Role: api.MessageRoleSystem}, capture, {ID: "procedure", Role: api.MessageRoleSystem}}}
	_, restored, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, history, req)
	testutil.FailErr(t, "restore active request", err)
	if len(restored.Messages) != 4 || restored.Messages[1].ID != opener.ID || !restored.Messages[1].ContextPinned {
		t.Fatalf("active request not restored in history slot: %+v", restored.Messages)
	}
	testutil.FailErr(t, "validate restored request", validateRequestIntent(history, restored.Messages))
	if err := validateRequestIntent(history, req.Messages); err == nil {
		t.Fatal("missing request passed validation")
	}
	if history[0].ContextPinned {
		t.Fatal("request preparation mutated canonical history")
	}
}

func TestRequestIntentSurvivesViewReplacementWithoutDuplication(t *testing.T) {
	opener := api.Message{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Run it in a container"}
	source := []api.Message{opener}
	rows := []api.Message{{ID: "summary", Role: api.MessageRoleAssistant}, opener}
	for range 3 {
		rows = messageview.PreserveRequestIntent(rows, source)
	}
	if len(rows) != 2 || !rows[1].ContextPinned {
		t.Fatalf("repeated projection = %+v", rows)
	}
	if got := messageview.PreserveRequestIntent(nil, source); len(got) != 1 || got[0].Content != opener.Content {
		t.Fatalf("empty projection = %+v", got)
	}
	if got := messageview.PreserveRequestIntent(rows, nil); len(got) != 2 {
		t.Fatal("missing source changed history")
	}
	testutil.FailErr(t, "validate host-only request", validateRequestIntent(nil, nil))
}

func TestRestoredRequestNamesOmittedHistory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	opener := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build a game"}
	middle := api.Message{ID: "earlier", Ord: 3, Role: api.MessageRoleAssistant, Content: "Earlier work"}
	latest := api.Message{ID: "latest", Ord: 4, Role: api.MessageRoleAssistant, Content: "Recent work"}
	source := []api.Message{opener, middle, latest}
	req := modelcall.CompletionRequest{Messages: []api.Message{latest}}
	_, restored, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, source, req)
	testutil.FailErr(t, "restore request and omission", err)
	if len(restored.Messages) != 3 || restored.Messages[0].ID != opener.ID || restored.Messages[1].ID != historyCoverageNoticeID || restored.Messages[2].ID != latest.ID {
		t.Fatalf("restored history = %+v", restored.Messages)
	}
	want, err := omissionNotice(t.Context(), nil, &api.Session{}, historyCoverageNoticeID, 3, 3)
	testutil.FailErr(t, "render missing interval", err)
	if restored.Messages[1].Content != want.Content {
		t.Fatal("omission did not identify missing history")
	}
	_, again, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, source, restored)
	testutil.FailErr(t, "prepare restored request again", err)
	if countID(again.Messages, historyCoverageNoticeID) != 1 {
		t.Fatal("repeated preparation duplicated omission")
	}
}
