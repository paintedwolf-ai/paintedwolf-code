// Package homestartadmin serves Home submissions: it reserves a launch, creates
// its draft project and preparing chat in the background, and fences the first
// prompt against Stop and replacement.
package homestartadmin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/homestart"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ProtocolVersion is the Home start contract this host serves.
const ProtocolVersion = 1

// Projects creates and announces the reserved draft project.
type Projects interface {
	CreateReserved(ctx context.Context, id string, params project.CreateParams, commit func(context.Context, *sql.Tx) error) (*project.Project, error)
}

// ProjectEffects runs the work that follows a committed project.
type ProjectEffects interface {
	PrepareCreatedProject(ctx context.Context, p *project.Project)
	PublishProjectCreated(ctx context.Context, p *project.Project)
}

// Sessions creates the reserved preparing chat.
type Sessions interface {
	CreateReservedSession(ctx context.Context, req wire.CreateSessionRequest, reservation sessionadmin.SessionReservation) (*wire.Session, error)
}

// FirstTurns stops an admitted first turn while it is still the chat's latest.
type FirstTurns interface {
	LockOperation(id string) func()
	HoldAdmission(sessionID string) func()
	Abort(ctx context.Context, sessionID, reason string) error
}

type Deps struct {
	Store          *homestart.Store
	People         people.OwnerSource
	Projects       Projects
	ProjectEffects ProjectEffects
	Sessions       Sessions
	SessionStore   sessionstore.SessionRepository
	LLMService     *llm.Service
	FirstTurns     FirstTurns
}

type Handler struct {
	deps       Deps
	responses  *httpio.Responder
	background *taskgroup.Group
	runs       *runs
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) *Handler {
	httpio.RequireDependencies("homestartadmin",
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "People", Present: deps.People != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "ProjectEffects", Present: deps.ProjectEffects != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "SessionStore", Present: deps.SessionStore != nil},
		httpio.Required{Name: "FirstTurns", Present: deps.FirstTurns != nil},
	)
	return &Handler{deps: deps, responses: responses, background: background, runs: newRuns()}
}

func (h *Handler) HandleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	caller, ok := people.Caller(r.Context())
	if !ok {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "no authenticated person")
		return
	}
	storeID, err := h.deps.Store.StoreID(r.Context())
	if err != nil {
		h.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.HomeStartCapabilities{
		ProtocolVersion: ProtocolVersion, StoreID: storeID, PersonID: caller.ID,
	})
}

func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {
	caller, ok := people.Caller(r.Context())
	if !ok {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "no authenticated person")
		return
	}
	var req wire.PutHomeStartRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	choices := homestart.NormalizeChoices(req.Choices)
	if !h.validChoices(w, r, choices) {
		return
	}
	rec, err := h.deps.Store.Reserve(r.Context(), chi.URLParam(r, "launch_id"), caller.ID, choices)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if rec.FailureCode != "" && rec.FailureResumable {
		if err := h.deps.Store.ClearResumableFailure(r.Context(), rec.LaunchID); err != nil {
			h.responses.InternalError(w, r, err)
			return
		}
		if rec, err = h.deps.Store.Load(r.Context(), rec.LaunchID); err != nil {
			h.responses.InternalError(w, r, err)
			return
		}
	}
	h.schedule(r.Context(), rec)
	h.writeRecord(w, r, http.StatusAccepted, rec)
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	caller, ok := people.Caller(r.Context())
	if !ok {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "no authenticated person")
		return
	}
	rec, err := h.deps.Store.Get(r.Context(), chi.URLParam(r, "launch_id"), caller.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeRecord(w, r, http.StatusOK, rec)
}

func (h *Handler) HandleStop(w http.ResponseWriter, r *http.Request) {
	caller, ok := people.Caller(r.Context())
	if !ok {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "no authenticated person")
		return
	}
	launchID := chi.URLParam(r, "launch_id")
	// Stop and first-prompt admission share the operation lock; a replaced
	// operation's admission is already fenced in the receipt transaction.
	operationID := launchID
	if current, err := h.deps.Store.Get(r.Context(), launchID, caller.ID); err == nil {
		operationID = current.PromptOperationID
	}
	unlock := h.deps.FirstTurns.LockOperation(operationID)
	rec, err := h.deps.Store.Stop(r.Context(), launchID, caller.ID)
	unlock()
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if rec.Disposition == homestart.DispositionActive {
		if err := h.stopFirstTurn(context.WithoutCancel(r.Context()), rec); err != nil {
			h.responses.InternalError(w, r, err)
			return
		}
	}
	h.writeRecord(w, r, http.StatusOK, rec)
}

func (h *Handler) HandleReplaceFirstPrompt(w http.ResponseWriter, r *http.Request) {
	caller, ok := people.Caller(r.Context())
	if !ok {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "no authenticated person")
		return
	}
	var req wire.ReplaceHomeStartFirstPromptRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	unlock := h.deps.FirstTurns.LockOperation(strings.TrimSpace(req.OperationID))
	rec, err := h.deps.Store.ReplaceFirstPrompt(r.Context(), chi.URLParam(r, "launch_id"), caller.ID,
		req.ExpectedRevision, req.OperationID, req.ReplacementOperationID)
	unlock()
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeRecord(w, r, http.StatusOK, rec)
}

// stopFirstTurn applies an after-admission Stop to the first turn only: it
// holds admission so no later turn can start being admitted, and acts only
// while the first receipt is the chat's latest and still running or queued.
func (h *Handler) stopFirstTurn(ctx context.Context, rec homestart.Record) error {
	release := h.deps.FirstTurns.HoldAdmission(rec.SessionID)
	defer release()
	latest, err := h.deps.Store.FirstTurnIsLatestLive(ctx, rec)
	if err != nil || !latest {
		return err
	}
	err = h.deps.FirstTurns.Abort(ctx, rec.SessionID, "user stopped")
	if errors.Is(err, sessionstore.ErrSessionNotFound) {
		return nil
	}
	return err
}

func (h *Handler) validChoices(w http.ResponseWriter, r *http.Request, choices wire.HomeStartChoices) bool {
	if choices.Mode != nil && *choices.Mode != wire.SessionModeAgent && *choices.Mode != wire.SessionModeAsk {
		requestscope.WriteSessionModeError(h.responses, w, sessionstore.ErrInvalidSessionMode)
		return false
	}
	if (choices.ProviderID == "") != (choices.Model == "") {
		h.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "provider_id and model must be set together")
		return false
	}
	if choices.ProviderID != "" && h.deps.LLMService != nil {
		if err := h.deps.LLMService.ValidateModelRef(r.Context(), llm.ModelRef{
			ProviderID: choices.ProviderID, Model: choices.Model,
		}, llm.PolicySlotCoordinator); err != nil {
			h.responses.ModelAssignmentError(w, r, err)
			return false
		}
	}
	return true
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, homestart.ErrNotFound):
		h.responses.Fail(w, wire.ApiErrorCodeHomeStartNotFound, "no Home start under this launch id")
	case errors.Is(err, homestart.ErrConflict):
		h.responses.Fail(w, wire.ApiErrorCodeHomeStartConflict, "this launch id does not match the request")
	case errors.Is(err, homestart.ErrClosed):
		h.responses.Fail(w, wire.ApiErrorCodeHomeStartClosed, "this Home start was stopped or retired")
	case errors.Is(err, homestart.ErrAdmitted):
		h.responses.Fail(w, wire.ApiErrorCodeHomeStartAdmitted, "the first prompt was already sent")
	default:
		h.responses.InternalError(w, r, err)
	}
}

func (h *Handler) writeRecord(w http.ResponseWriter, r *http.Request, status int, rec homestart.Record) {
	out, err := h.project(r.Context(), rec)
	if err != nil {
		h.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, status, out)
}

func (h *Handler) project(ctx context.Context, rec homestart.Record) (wire.HomeStart, error) {
	out := wire.HomeStart{
		LaunchID: rec.LaunchID, Phase: phaseOf(rec), Revision: rec.Revision, Choices: rec.Choices,
		FirstPrompt: wire.HomeStartFirstPrompt{OperationID: rec.PromptOperationID},
	}
	if rec.FailureCode != "" && rec.Disposition == homestart.DispositionActive {
		out.Failure = &wire.HomeStartFailure{Code: rec.FailureCode, Resumable: rec.FailureResumable}
	}
	if rec.Progress != homestart.ProgressReserved {
		out.ProjectID = rec.ProjectID
	}
	if rec.Progress == homestart.ProgressSessionBound {
		out.SessionID = rec.SessionID
		if sess, err := h.deps.SessionStore.Get(ctx, rec.SessionID); err == nil {
			out.SessionStatus = sess.Status
		} else if !errors.Is(err, sessionstore.ErrSessionNotFound) {
			return wire.HomeStart{}, err
		}
		admitted, status, err := h.deps.Store.FirstPrompt(ctx, rec)
		if err != nil {
			return wire.HomeStart{}, err
		}
		out.FirstPrompt.Admitted, out.FirstPrompt.Status = admitted, status
	}
	return out, nil
}

func phaseOf(rec homestart.Record) string {
	switch {
	case rec.Disposition == homestart.DispositionRetired:
		return "retired"
	case rec.Disposition == homestart.DispositionStopped:
		return "stopped"
	case rec.FailureCode != "":
		return "failed"
	default:
		return string(rec.Progress)
	}
}

// HostFirstTurns stops first turns through the session host's admission and
// stop lifecycle.
func HostFirstTurns(host *session.Host) FirstTurns {
	return hostFirstTurns{host: host}
}

type hostFirstTurns struct{ host *session.Host }

func (t hostFirstTurns) LockOperation(id string) func() { return t.host.Submissions.LockOperation(id) }

func (t hostFirstTurns) HoldAdmission(sessionID string) func() {
	return t.host.Submissions.HoldAdmission(sessionID)
}

func (t hostFirstTurns) Abort(ctx context.Context, sessionID, reason string) error {
	return t.host.Stops.Abort(ctx, sessionID, reason)
}
