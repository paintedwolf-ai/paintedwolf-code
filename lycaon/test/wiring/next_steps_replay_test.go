package wiring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// replaySession drives a real host with a scripted model that follows what the
// host offers, the way a reader of the refusal would.
type replaySession struct {
	*commandProgramSession
	config *llm.MockConfig
}

func newReplaySession(t *testing.T) *replaySession {
	t.Helper()
	config := &llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: "run the program", FollowUpText: "Done."}}}
	base := startCommandProgramSessionWithClient(t, llm.NewMockProvider(config))
	base.h.SeedProgress(t, t.Context(), base.session)
	return &replaySession{commandProgramSession: base, config: config}
}

// call submits one tool call and returns its delivered result message.
func (s *replaySession) call(t *testing.T, tool string, args map[string]any) *wire.ToolResult {
	t.Helper()
	s.config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: t.Name() + tool, Name: tool, Args: args}}
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleTool && msgs[i].ToolResult != nil && msgs[i].ToolResult.Tool == tool {
			return msgs[i].ToolResult
		}
	}
	t.Fatalf("no result for %s", tool)
	return nil
}

func nextCalls(t *testing.T, result *wire.ToolResult) []toolreach.Call {
	t.Helper()
	var out []toolreach.Call
	for _, feedback := range result.Feedback {
		raw, present := feedback.Details[wire.ToolFeedbackNextCalls]
		if !present {
			continue
		}
		encoded, err := json.Marshal(raw)
		testutil.FailErr(t, "encode next calls", err)
		var calls []toolreach.Call
		testutil.FailErr(t, "decode next calls", json.Unmarshal(encoded, &calls))
		out = append(out, calls...)
	}
	return out
}

// followExactCalls makes every call the host offered complete, and requires each
// to run. A step the reader must finish is not submitted as written.
func (s *replaySession) followExactCalls(t *testing.T, calls []toolreach.Call) int {
	t.Helper()
	followed := 0
	for _, call := range calls {
		if call.Partial {
			continue
		}
		followed++
		if result := s.call(t, call.Tool, call.Args); result.Outcome == wire.ToolResultOutcomeRejected {
			t.Errorf("offered call %s was refused: %+v", call.Text(), result.Feedback)
		}
	}
	return followed
}

func TestOfferedCallsWorkWhenFollowed(t *testing.T) {
	t.Run("an image call on a directory offers a listing that runs", func(t *testing.T) {
		s := newReplaySession(t)
		testutil.FailErr(t, "seed directory", os.Mkdir(filepath.Join(s.dir, "sub.png"), 0o755))
		result := s.call(t, "view_image", map[string]any{"path": "sub.png"})
		if s.followExactCalls(t, nextCalls(t, result)) == 0 {
			t.Fatalf("no exact listing offered: %+v", result.Feedback)
		}
	})
	t.Run("a restore with no history offers a history call that runs once finished", func(t *testing.T) {
		s := newReplaySession(t)
		result := s.call(t, "restore_version", map[string]any{"path": "notes.txt"})
		calls := nextCalls(t, result)
		if len(calls) != 1 || calls[0].Tool != "source_history" {
			t.Fatalf("next calls = %+v, feedback %+v", calls, result.Feedback)
		}
		args := calls[0].Args
		args["path"] = "notes.txt"
		if ran := s.call(t, calls[0].Tool, args); ran.Outcome == wire.ToolResultOutcomeRejected {
			t.Fatalf("offered call was refused: %+v", ran.Feedback)
		}
	})
}

// Advisories raised by a native operation reach the reader through command.
func TestProgramOperationAdvisoriesReachTheReader(t *testing.T) {
	s := newReplaySession(t)
	testutil.FailErr(t, "seed editorconfig", os.WriteFile(filepath.Join(s.dir, ".editorconfig"), []byte("root = true\n[*]\ntrim_trailing_whitespace = true\n"), 0o644))
	result := s.call(t, "command", map[string]any{"command": "edit --path notes.txt --old-string 'first note' --new-string 'first note  '"})
	if result.Outcome == wire.ToolResultOutcomeRejected {
		t.Fatalf("write form refused: %+v", result.Feedback)
	}
	var codes []string
	for _, feedback := range result.Feedback {
		codes = append(codes, feedback.Code)
	}
	found := false
	for _, code := range codes {
		found = found || code == "EDITORCONFIG_MISMATCH"
	}
	if !found {
		t.Fatalf("advisory did not reach the reader: %v", codes)
	}
}
