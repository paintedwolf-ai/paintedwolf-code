//go:build darwin || linux

package commands

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
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

// directIPCommandSession starts a session whose one command asks for direct
// network access; with quieted false it revokes the harness quiets so the
// direct-IP ask reaches a card.
func directIPCommandSession(t *testing.T, quieted bool) *commandProgramSession {
	t.Helper()
	s := newCommandProgramSessionWithArgs(t, map[string]any{
		"command":            "go version",
		"capability_request": map[string]any{"direct_ip": true},
	}, wire.CreateSessionRequest{}, "")
	if !quieted {
		gate := s.h.Server.Admin.Capabilities.Access.Gate
		for _, q := range gate.ListAskQuiets(s.session) {
			if !gate.RevokeAskQuiet(q.ID) {
				t.Fatalf("revoke quiet %+v", q)
			}
		}
	}
	return s
}

// requireDirectIPAuthzRows asserts the run left direct_ip_started and
// direct_ip_completed rows whose detail names unobserved direct access.
func requireDirectIPAuthzRows(t *testing.T, s *commandProgramSession) {
	t.Helper()
	rows := visibilityAuthzRows(t, s)
	for _, action := range []authzcontext.EventAction{authzcontext.EventActionDirectIPStarted, authzcontext.EventActionDirectIPCompleted} {
		row := visibilityAuthzRowFor(rows, action)
		if row == nil {
			t.Fatalf("no %s row in %+v", action, rows)
		}
		ea := row.Detail.ExternalAccess
		if ea == nil || !slices.Contains(ea.Modes, wire.ExternalAccessModeDirectIP) || ea.VisibilitySummary != wire.ExternalAccessVisibilitySummaryUnobserved {
			t.Fatalf("%s detail does not name unobserved direct access: %+v", action, ea)
		}
	}
}

// A direct-IP command answered on its card runs and leaves its direct rows.
func TestNonHTTPContractMandatoryAuthzDetailForDirect(t *testing.T) {
	s := directIPCommandSession(t, false)
	cards := runCommandWithApprovals(t, s, "recommended")
	if cards[0].ToolApproval.Plan.Subject.Kind != wire.ApprovalSubjectKind(hitl.ApprovalSubjectDirectIP) {
		t.Fatalf("card subject = %q, want direct_ip", cards[0].ToolApproval.Plan.Subject.Kind)
	}
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 {
		t.Fatalf("direct command failed: %+v", outcome)
	}
	requireDirectIPAuthzRows(t, s)
}

// A quieted direct-IP ask runs the command without a card, as the quiet
// answers it, and still records the direct rows.
func TestQuietedDirectIPAskRunsTheCommand(t *testing.T) {
	s := directIPCommandSession(t, true)
	runCommandWithApprovals(t, s)
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 {
		t.Fatalf("quieted direct command failed: %+v", outcome)
	}
	requireDirectIPAuthzRows(t, s)
}
