package sessionadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// PythonEvidenceReader supplies persisted Portable Python Den projections,
// retained output streams, and admission records from the host runtime.
type PythonEvidenceReader interface {
	PythonDenProjection(executionID string, scope pythonevidence.ViewerScope) (pythonevidence.PythonActivityProjection, error)
	ReadPythonRetainedOutput(ctx context.Context, scope pythonevidence.ViewerScope, artifactID string) ([]byte, error)
	PythonPersistedAdmissions(executionID string, scope pythonevidence.ViewerScope) ([]pythonevidence.LedgerAdmission, error)
}

// ViewerScopeForSession derives the fail-closed pythonevidence.ViewerScope
// strictly from the authenticated session record, using the session's actual
// unique ID so sibling workers sharing an AgentType and root session remain
// strictly isolated and match the writer's session/actor/view identity.
func ViewerScopeForSession(sess *wire.Session) pythonevidence.ViewerScope {
	if sess == nil {
		return pythonevidence.ViewerScope{}
	}
	view := string(wire.SourceWorkspaceKindProject)
	if strings.TrimSpace(sess.ParentSessionID) != "" {
		actor := strings.TrimSpace(sess.AgentType)
		if actor == "" {
			actor = "worker"
		}
		return pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleWorker,
			SessionID: strings.TrimSpace(sess.ID),
			Actor:     actor,
			View:      view,
		}
	}
	actor := strings.TrimSpace(sess.AgentType)
	if actor == "" {
		actor = "coordinator"
	}
	return pythonevidence.ViewerScope{
		Role:      pythonevidence.RoleCoordinator,
		SessionID: strings.TrimSpace(sess.ID),
		Actor:     actor,
		View:      view,
	}
}

func (s *Transcript) servePythonProgramReceipt(w http.ResponseWriter, r *http.Request, sess *wire.Session, executionID string) bool {
	if s.PythonEvidence == nil {
		return false
	}
	scope := ViewerScopeForSession(sess)
	proj, err := s.PythonEvidence.PythonDenProjection(executionID, scope)
	if err != nil {
		switch {
		case errors.Is(err, portablepython.ErrExecutionNotFound):
			return false
		case errors.Is(err, portablepython.ErrAmbiguousExecutionID):
			s.responses.Fail(w, wire.ApiErrorCodeForbidden, "python activity scope is ambiguous")
			return true
		default:
			s.responses.InternalError(w, r, err)
			return true
		}
	}
	if !proj.Visible || proj.RedactedForVisibility {
		s.responses.Fail(w, wire.ApiErrorCodeForbidden, "python activity belongs to another session or worker")
		return true
	}
	adms, err := s.PythonEvidence.PythonPersistedAdmissions(executionID, scope)
	if err != nil {
		if errors.Is(err, pythonevidence.ErrAccessDenied) || errors.Is(err, portablepython.ErrAmbiguousExecutionID) {
			s.responses.Fail(w, wire.ApiErrorCodeForbidden, "python activity belongs to another session or worker")
			return true
		}
		s.responses.InternalError(w, r, err)
		return true
	}
	page := buildPythonProgramReceiptPage(proj, adms)
	httpio.WriteJSON(w, http.StatusOK, page)
	return true
}

func buildPythonProgramReceiptPage(
	proj pythonevidence.PythonActivityProjection,
	adms []pythonevidence.LedgerAdmission,
) wire.CommandProgramReceiptPage {
	exitCode := proj.ExitCode
	state := "completed"
	stopCode := ""
	if !proj.CleanCompletion {
		state = "stopped"
	}
	if proj.Stop != nil {
		stopCode = string(proj.Stop.Code)
	}

	activities := make([]wire.CommandActivity, 0, len(proj.PublishedDiffs)+len(proj.RefusedChildren))
	for _, diff := range proj.PublishedDiffs {
		activities = append(activities, wire.CommandActivity{
			ID:               diff.HostPublicationID,
			Group:            "workspace_write",
			Phase:            "publication",
			Label:            string(diff.AttributionKind),
			Subject:          diff.Target.RelPath,
			Outcome:          "completed",
			State:            "completed",
			ExecutionCount:   1,
			CompletedEffects: 1,
			ExecutionIDs:     []string{proj.ExecutionID},
			Spans:            []wire.CommandActivitySpan{},
		})
	}
	for _, ref := range proj.RefusedChildren {
		activities = append(activities, wire.CommandActivity{
			ID:             ref.OperationID,
			Group:          "refusal",
			Phase:          "admission",
			Label:          string(ref.StopCode),
			Subject:        ref.Target.RelPath,
			Outcome:        "refused",
			State:          "refused",
			ExecutionCount: 1,
			FailedCount:    1,
			ExecutionIDs:   []string{ref.OperationID},
			Spans:          []wire.CommandActivitySpan{},
		})
	}

	executions := make([]wire.CommandExecutionReceipt, 0, len(adms))
	for _, adm := range adms {
		executions = append(executions, wire.CommandExecutionReceipt{
			ID:                adm.Record.OperationID,
			ProgramID:         proj.ExecutionID,
			ParentExecutionID: adm.ParentOperationID,
			Owner:             "python",
			Command:           adm.Record.Operation,
			State:             string(adm.Record.Decision),
			OwnerStarted:      true,
			OwnerInvoked:      true,
		})
	}

	return wire.CommandProgramReceiptPage{
		Program: wire.CommandProgramReceipt{
			ID:            proj.ExecutionID,
			State:         state,
			StatusCode:    &exitCode,
			StopCode:      stopCode,
			ProgressState: state,
			Activities:    activities,
		},
		Executions: executions,
		Nodes:      []wire.CommandProgramNode{},
		Activities: activities,
	}
}
