package wiring

import (
	"encoding/json"
	"strings"
	"testing"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func settledCommandProgramOutcome(t *testing.T, s *commandProgramSession) (command.ProgramOutcome, bool) {
	t.Helper()
	messages, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read returned program", err)
	var result string
	for _, raw := range messages {
		msg := messageview.RedactMessage(raw)
		if msg.Role == wire.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.ToolCallID == "program-1" {
			result = msg.ToolResult.Content
			break
		}
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(result)
	if !ok {
		t.Fatalf("returned program has no JSON body: %q", result)
	}
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode returned program", json.NewDecoder(strings.NewReader(body)).Decode(&outcome))
	wasParked := outcome.State == "parked"
	if wasParked {
		var parked command.ParkedProgramResult
		testutil.FailErr(t, "decode parked call", json.NewDecoder(strings.NewReader(body)).Decode(&parked))
		var winner awaitstore.Condition
		testutil.WaitFor(t, func() bool {
			var raw string
			testutil.FailErr(t, "read program wait", s.h.DB.QueryRowContext(t.Context(), `SELECT coalesce(winner_json,'') FROM wait_leases WHERE id=?`, parked.LeaseID).Scan(&raw))
			if raw == "" {
				return false
			}
			testutil.FailErr(t, "decode program wake", json.Unmarshal([]byte(raw), &winner))
			return true
		})
		if winner.ProgramID != parked.ProgramID || winner.Kind != "program_done" {
			t.Fatalf("program wake=%+v", winner)
		}
		result = winner.Report
		testutil.FailErr(t, "decode final program", json.Unmarshal([]byte(result), &outcome))
	}
	if outcome.State != "completed" || outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("program failed: %+v refusal=%+v", outcome, outcome.Refusal)
	}
	return outcome, wasParked
}
