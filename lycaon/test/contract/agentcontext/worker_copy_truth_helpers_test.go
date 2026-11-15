package contract

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type workerStateMatrixCell struct {
	name  string
	facts workerstate.Facts
	view  workerstate.View
}

// derivedWorkerStateMatrix generates reachable cells from workerstate.Derive
// rather than listing states by hand.
func derivedWorkerStateMatrix() []workerStateMatrixCell {
	rounds := []*workerstate.Round{
		nil,
		{Settled: true, Reported: true},
		{Settled: true, Reported: false},
		{Settled: false, Held: true},
		{Settled: false, DecisionPending: true},
		{Settled: false, ApprovalWait: true},
		{Settled: false, Parked: true},
		{Settled: false, Queued: true},
		{Settled: false},
	}
	var out []workerStateMatrixCell
	seen := map[string]bool{}
	for _, closed := range []bool{false, true} {
		for _, unusable := range []bool{false, true} {
			for _, planned := range []bool{false, true} {
				for _, recovery := range []bool{false, true} {
					for _, role := range []workerstate.Role{workerstate.RoleUnbound, workerstate.RoleReviewer, workerstate.RolePlanned} {
						for _, r := range rounds {
							if planned && r != nil {
								continue
							}
							for _, unaccounted := range []bool{false, true} {
								for _, changeKind := range []string{"none", "open", "shelved"} {
									if r == nil && (unaccounted || changeKind != "none") {
										continue
									}
									f := workerstate.Facts{
										Closed:              closed,
										SessionUnusable:     unusable,
										Planned:             planned,
										Role:                role,
										Recovery:            recovery,
										Latest:              r,
										UnaccountedComplete: unaccounted,
										OpenChanges:         changeKind == "open",
										ShelvedChanges:      changeKind == "shelved",
									}
									v := workerstate.Derive(f)
									opsKey := make([]string, 0, len(v.Ops))
									for _, op := range v.Ops {
										opsKey = append(opsKey, string(op))
									}
									key := fmt.Sprintf("%s|%s|%s|%s|%t|%s", v.State, v.Reason, role, strings.Join(opsKey, ","), unaccounted, changeKind)
									if seen[key] {
										continue
									}
									seen[key] = true
									out = append(out, workerStateMatrixCell{
										name:  fmt.Sprintf("%s_%s_%s_%s", v.State, v.Reason, role, changeKind),
										facts: f,
										view:  v,
									})
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

func rosterMapForCell(cell workerStateMatrixCell, workerName, handle, workspace string) map[string]any {
	agent := "implementer"
	scopeMode := "write"
	if cell.facts.Role == workerstate.RoleReviewer {
		agent = "code-reviewer"
		scopeMode = "read"
	}
	row := map[string]any{
		"name":            workerName,
		"agent":           agent,
		"scope_mode":      scopeMode,
		"workspace_state": workspace,
		"state":           string(cell.view.State),
		"reason":          string(cell.view.Reason),
		"about":           "verify auth boundary",
		"read":            []string{"internal/auth/token.go"},
	}
	if cell.view.State == workerstate.Asking {
		row["question"] = "Which audience claim applies?"
	}
	if cell.facts.Latest != nil && cell.facts.Latest.Parked {
		row["parked_reason"] = "process_done"
	}
	if handle != "" && (cell.facts.OpenChanges || cell.facts.ShelvedChanges || cell.facts.UnaccountedComplete || cell.view.State == workerstate.Failed) {
		disp := "accounted"
		switch {
		case cell.facts.OpenChanges:
			disp = "open"
		case cell.facts.ShelvedChanges:
			disp = "shelved"
			row["shelve_reason"] = "waiting on upstream check"
		case cell.facts.UnaccountedComplete:
			disp = "unaccounted"
		}
		row["result_handle"] = handle
		row["result_disposition"] = disp
		if cell.facts.OpenChanges || cell.facts.ShelvedChanges {
			row["changed"] = []string{"internal/auth/token.go"}
		}
	}
	return row
}

var workerToolOps = map[string]workerstate.Operation{
	"instruct_worker": workerstate.OpInstruct,
	"answer_decision": workerstate.OpAnswer,
	"preview_overlay": workerstate.OpPreview,
	"promote_overlay": workerstate.OpPromote,
	"reject_overlay":  workerstate.OpReject,
	"shelve_overlay":  workerstate.OpShelve,
	"worker_history":  workerstate.OpHistory,
}

var volatilePromptVars = map[string]bool{
	"workers": true, "workers_omitted": true, "workers_asking": true,
	"workers_delivered": true, "workers_failed": true, "workers_idle": true,
	"worker_result": true, "worker_digest": true, "last_worker_decision_request": true,
	"message_count": true, "has_needs_reply": true, "round_instructions": true,
	"quiet_minutes": true, "model_calls": true, "estimated_spend_usd": true,
	"spend_under_cent": true, "latest_message_seq": true, "progress_snapshot": true,
	"progress_closure_armed": true, "earlier_results": true, "instruction_count": true,
	"prior_change": true,
}

var (
	pongoTagRE   = regexp.MustCompile(`\{\{([^}]*)\}\}|\{%([^%]*)%\}`)
	pongoIdentRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	pongoKw      = map[string]bool{
		"if": true, "elif": true, "else": true, "endif": true, "for": true,
		"in": true, "endfor": true, "and": true, "or": true, "not": true,
		"true": true, "false": true, "include": true, "set": true, "forloop": true,
		"comment": true, "endcomment": true, "verbatim": true, "endverbatim": true,
	}
)

func extractTemplateRootVars(source string) []string {
	stripped := regexp.MustCompile(`"[^"]*"|'[^']*'`).ReplaceAllString(source, `""`)
	locals := map[string]bool{}
	var vars []string
	for _, m := range pongoTagRE.FindAllStringSubmatch(stripped, -1) {
		body := strings.TrimSpace(m[1] + m[2])
		fields := strings.Fields(body)
		if len(fields) >= 2 && fields[0] == "set" {
			locals[strings.TrimSuffix(fields[1], "=")] = true
			continue
		}
		if len(fields) >= 4 && fields[0] == "for" && fields[2] == "in" {
			locals[fields[1]] = true
			body = strings.Join(fields[3:], " ")
		}
		for _, tok := range strings.FieldsFunc(body, func(r rune) bool {
			return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.')
		}) {
			root := strings.SplitN(tok, ".", 2)[0]
			if !pongoIdentRE.MatchString(root) || pongoKw[root] || locals[root] || slices.Contains(vars, root) {
				continue
			}
			vars = append(vars, root)
		}
	}
	return vars
}

type admissionJobMap map[string]*api.WorkerTask

func (m admissionJobMap) Get(id string) (*api.WorkerTask, bool) { v, ok := m[id]; return v, ok }

type admissionDecisionMap map[string]api.WorkerDecisionRequest

func (m admissionDecisionMap) Get(_ context.Context, child string) (api.WorkerDecisionRequest, bool, error) {
	v, ok := m[child]
	return v, ok, nil
}

func newAdmissionService(t *testing.T) (*db.Store, *workerrecords.Store, *workerops.Service, admissionJobMap, admissionDecisionMap, workerrecords.Actor) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "copy_admission.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	ts := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'implementer', ?, ?, ?)`, testdbseed.DefaultProjectID, ts, ts, ts)
	contractcheck.FailErr(t, "insert child session", err)
	records := workerrecords.New(sqlDB)
	jobs := admissionJobMap{}
	decs := admissionDecisionMap{}
	actor := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	svc := &workerops.Service{
		Reader:     workerops.Reader{Records: records, Jobs: jobs, Decisions: decs},
		Release:    func(context.Context, string) error { return nil },
		WakeParked: func(context.Context, string) error { return nil },
		Agents: func(agent string) (bool, bool) {
			switch agent {
			case "implementer":
				return true, true
			case "repo-researcher", "code-reviewer":
				return false, true
			}
			return false, false
		},
	}
	return sqlDB, records, svc, jobs, decs, actor
}
