//go:build darwin || linux

package wiring

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// visibilityAuthzRow is one persisted authorization ledger row.
type visibilityAuthzRow struct {
	Action authzcontext.EventAction
	Detail authzcontext.EventDetail
}

func visibilityAuthzRows(t *testing.T, s *commandProgramSession) []visibilityAuthzRow {
	t.Helper()
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT action, detail_json FROM authz_events WHERE session_id = ? ORDER BY event_seq`, s.session)
	testutil.FailErr(t, "read authz events", err)
	defer rows.Close()
	var out []visibilityAuthzRow
	for rows.Next() {
		var action, detail string
		testutil.FailErr(t, "scan authz event", rows.Scan(&action, &detail))
		row := visibilityAuthzRow{Action: authzcontext.EventAction(action)}
		if detail != "" {
			testutil.FailErr(t, "decode authz detail", json.Unmarshal([]byte(detail), &row.Detail))
		}
		out = append(out, row)
	}
	testutil.FailErr(t, "iterate authz events", rows.Err())
	return out
}

func visibilityAuthzRowFor(rows []visibilityAuthzRow, action authzcontext.EventAction) *visibilityAuthzRow {
	for i := range rows {
		if rows[i].Action == action {
			return &rows[i]
		}
	}
	return nil
}

// An applied socket grant leaves a capability_applied row naming the socket.
func TestNonHTTPContractMandatoryAuthzDetailForSocket(t *testing.T) {
	path, hits := serveDaemonSocket(t)
	s := newCommandProgramSessionWithArgs(t, map[string]any{
		"command": "curl -s --max-time 5 --unix-socket '" + path + "' http://localhost/version",
	}, wire.CreateSessionRequest{}, "")
	runCommandWithApprovals(t, s, "recommended")
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || hits.Load() != 1 {
		t.Fatalf("curl did not reach the daemon: requests=%d outcome=%+v", hits.Load(), outcome)
	}
	rows := visibilityAuthzRows(t, s)
	applied := visibilityAuthzRowFor(rows, authzcontext.EventActionCapabilityApplied)
	if applied == nil {
		t.Fatalf("no capability_applied row in %+v", rows)
	}
	ea := applied.Detail.ExternalAccess
	if ea == nil || len(ea.Sockets) != 1 || ea.Sockets[0].ResolvedPath == "" {
		t.Fatalf("capability_applied detail does not name the socket: %+v", ea)
	}
	if ea.VisibilitySummary != wire.ExternalAccessVisibilitySummaryUnobserved {
		t.Fatalf("socket visibility = %q", ea.VisibilitySummary)
	}
}
