package session

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func userMsg(content string) api.Message {
	return api.Message{Role: api.MessageRoleUser, Content: content, Visibility: api.MessageVisibilityTranscript}
}

func hostKickMsg(content string) api.Message {
	return api.Message{Role: api.MessageRoleUser, Content: content, Visibility: api.MessageVisibilityInternal}
}

func editToolMsg(path string) api.Message {
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content:  "Edited " + path,
			Outcome:  api.ToolResultOutcomeCompleted,
			FileEdit: &api.FileEditSnapshot{Path: path, After: "x"},
		},
	}
}

func readToolMsg() api.Message {
	return api.Message{
		Role:       api.MessageRoleTool,
		ToolResult: &api.ToolResult{Content: "1\tline", Outcome: api.ToolResultOutcomeCompleted},
	}
}

func assistantMsg(content string) api.Message {
	return api.Message{Role: api.MessageRoleAssistant, Content: content}
}

func TestEditFollowUpRepeatFiresOnSecondConsecutiveFollowUp(t *testing.T) {
	history := []api.Message{
		userMsg("add AI to this"),
		editToolMsg("index.html"),
		assistantMsg("Done!"),
		userMsg("it appears broken"),
		readToolMsg(),
		editToolMsg("index.html"),
		assistantMsg("Fixed!"),
	}
	if !editFollowUpRepeat(history) {
		t.Fatal("expected kick on second consecutive follow-up after inline edits")
	}
}

func TestEditFollowUpRepeatQuietOnFirstFollowUp(t *testing.T) {
	history := []api.Message{
		userMsg("add AI to this"),
		editToolMsg("index.html"),
		assistantMsg("Done!"),
	}
	if editFollowUpRepeat(history) {
		t.Fatal("first follow-up after edits must not kick")
	}
}

func TestEditFollowUpRepeatQuietWhenLastTurnHadNoEdits(t *testing.T) {
	history := []api.Message{
		userMsg("add AI to this"),
		editToolMsg("index.html"),
		userMsg("it appears broken"),
		readToolMsg(),
		assistantMsg("Here is my diagnosis — no edits yet."),
	}
	if editFollowUpRepeat(history) {
		t.Fatal("survey-only turn must not kick")
	}
}

func TestEditFollowUpRepeatQuietWhenEarlierTurnHadNoEdits(t *testing.T) {
	history := []api.Message{
		userMsg("what does this code do?"),
		readToolMsg(),
		assistantMsg("It renders a board."),
		userMsg("now fix the bug"),
		editToolMsg("index.html"),
		assistantMsg("Fixed!"),
	}
	if editFollowUpRepeat(history) {
		t.Fatal("edits on only the most recent turn must not kick")
	}
}

func TestEditFollowUpRepeatIgnoresHostTurnsAsDelimiters(t *testing.T) {
	history := []api.Message{
		userMsg("add AI to this"),
		editToolMsg("index.html"),
		hostKickMsg("[host: worker finished]"),
		assistantMsg("Acknowledged."),
		userMsg("it appears broken"),
		editToolMsg("index.html"),
		assistantMsg("Fixed!"),
	}
	if !editFollowUpRepeat(history) {
		t.Fatal("internal host messages must not delimit user turns")
	}
}

func TestEditFollowUpRepeatEmptyHistory(t *testing.T) {
	if editFollowUpRepeat(nil) {
		t.Fatal("empty history must not kick")
	}
}
