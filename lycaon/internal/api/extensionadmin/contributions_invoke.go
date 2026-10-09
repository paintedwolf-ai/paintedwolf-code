package extensionadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/editorturn"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/usernotice"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// invokeScope is one invocation, resolved from its route.
type invokeScope struct {
	route      contribution.InvocationScope
	projectID  string
	projectDir string
	roots      []string
	sess       *wire.Session
	project    *project.Project
}

func (s *Handler) HandleInvokeProjectCommand(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	s.invokeCommand(w, r, invokeScope{
		route:      contribution.InvocationProject,
		projectID:  p.ID,
		projectDir: project.PrimaryRootPath(p),
		roots:      project.RootPaths(p),
		project:    p,
	})
}

func (s *Handler) HandleInvokeSessionCommand(w http.ResponseWriter, r *http.Request) {
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p, _ := s.Projects.Get(r.Context(), sess.ProjectID)
	s.invokeCommand(w, r, invokeScope{
		route:      contribution.InvocationSession,
		projectID:  sess.ProjectID,
		projectDir: sess.WorkspacePath,
		roots:      []string{sess.WorkspacePath},
		sess:       sess,
		project:    p,
	})
}

// invokeCommand admits one captured command before execution.
func (s *Handler) invokeCommand(w http.ResponseWriter, r *http.Request, scope invokeScope) {
	var req wire.CommandInvokeRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	req.OperationID = operationID.String()
	unlockOperation := s.Sessions.Submissions.LockOperation(req.OperationID)
	defer unlockOperation()
	if strings.TrimSpace(req.FrameRevision) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "frame_revision is required")
		return
	}
	commandID, err := contribution.ParseID(httpio.EncodedPathID(r, "command_id"))
	if err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "command_id", "reason": "is not a contribution id"}, "command_id is not a contribution id")
		return
	}

	scopeKey := scope.projectID
	if scope.sess != nil {
		scopeKey = scope.sess.ID
	}
	digest, err := commandinvoke.RequestDigest(string(scope.route), scopeKey, commandID.String(), req)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if receipt, found, err := s.Runtime.Receipts.Load(r.Context(), req.OperationID); err != nil {
		s.responses.InternalError(w, r, err)
		return
	} else if found {
		if receipt.InputDigest != digest {
			s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for a different invocation")
			return
		}
		status, body, err := commandinvoke.DecodeResponse(receipt.ResponseJSON)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		s.WriteCommandResponse(w, status, body)
		return
	}

	frame, err := s.CaptureContributionFrame(r.Context(), scope.projectID, scope.projectDir)
	if err != nil {
		s.writeContributionFrameError(w, r, err)
		return
	}
	command, ok := frame.Command(commandID)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeCommandNotFound, "command "+commandID.String()+" is not in the current contribution frame")
		return
	}
	resolved, ok := frame.View.Contributions.ResolveCommand(command)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "the command's operation graph cannot be resolved")
		return
	}
	if contribution.InvocationScopeFor(resolved.Action.Kind) != scope.route {
		s.responses.Fail(w, wire.ApiErrorCodeInvocationScopeMismatch,
			"action kind "+string(resolved.Action.Kind)+" does not invoke on this route")
		return
	}
	if req.FrameRevision != frame.Revision && !s.subgraphUnchanged(req.FrameRevision, frame, commandID) {
		s.responses.Fail(w, wire.ApiErrorCodeContributionFrameChanged,
			"the invoked command changed since the caller's frame; refresh and retry deliberately")
		return
	}

	invokeCtx := wire.CommandInvokeContext{}
	if req.Context != nil {
		invokeCtx = *req.Context
	}
	if contribution.EvaluateHostCondition(command.When, hostFactLookup(frame, scope.sess, invokeCtx)) == contribution.HostFalse {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "the command's condition does not hold")
		return
	}
	if contribution.EvaluateHostCondition(command.Enablement, hostFactLookup(frame, scope.sess, invokeCtx)) == contribution.HostFalse {
		s.responses.Fail(w, wire.ApiErrorCodeCommandDisabled, "the command's enablement condition does not hold")
		return
	}
	inputFields := resolved.Input
	if command.Interaction != nil {
		inputFields = contribution.InteractionFieldsForAnswers(command.Interaction, req.Args)
	}
	validatedArgs, err := commandinvoke.ValidateArguments(inputFields, req.Args, projectPathResolver(scope.project))
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidArguments, "the command arguments are invalid")
		return
	}
	if err := commandinvoke.ValidateRequiredConfirmations(command.Interaction, inputFields, validatedArgs); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidArguments, "the command arguments are invalid")
		return
	}
	if resolved.Action.Kind == contribution.ActionEditorAction {
		if instruction, ok := validatedArgs["instruction"].(string); ok {
			invokeCtx.Instruction = instruction
		}
	}

	in := commandinvoke.Input{
		Frame:     frame,
		CommandID: commandID,
		Command:   command,
		Resolved:  resolved,
		ProjectID: scope.projectID,
		Context:   invokeCtx,
		Args:      validatedArgs,
	}
	if scope.sess != nil {
		in.SessionID = scope.sess.ID
	}
	if err := s.Runtime.Authority.Authorize(r.Context(), in); err != nil {
		var denial *commandinvoke.Denial
		if errors.As(err, &denial) {
			s.responses.FailReason(w, wire.ApiErrorCodeContributionNotAuthorized, denial.Reason)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}

	status, body, handled := s.executeCommand(w, r, scope, req, in)
	if !handled {
		return
	}
	body.FrameRevision = frame.Revision
	encoded, err := commandinvoke.EncodeResponse(status, body)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if err := s.Runtime.Receipts.Save(r.Context(), commandinvoke.Receipt{
		OperationID: req.OperationID, InputDigest: digest, ResponseJSON: encoded,
	}); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.WriteCommandResponse(w, status, body)
}

// Failed receipts retain command state internally, while HTTP failures use the
// same notice envelope on initial delivery and idempotent replay.
func (s *Handler) WriteCommandResponse(w http.ResponseWriter, status int, body wire.CommandInvokeResponse) {
	if status >= http.StatusBadRequest && body.Error != nil {
		s.responses.Fail(w, body.Error.Code, body.Error.Message)
		return
	}
	httpio.WriteJSON(w, status, body)
}

// subgraphUnchanged compares retained execution dependencies.
func (s *Handler) subgraphUnchanged(callerRevision string, current *contribframe.Frame, id contribution.ID) bool {
	prior, ok := s.contributionFrameByRevision(callerRevision)
	if !ok {
		return false
	}
	priorIdentity, ok := prior.SubgraphIdentity(id)
	if !ok {
		return false
	}
	currentIdentity, ok := current.SubgraphIdentity(id)
	return ok && priorIdentity == currentIdentity
}

// executeCommand dispatches one compiled action.
func (s *Handler) executeCommand(
	w http.ResponseWriter, r *http.Request,
	scope invokeScope, req wire.CommandInvokeRequest, in commandinvoke.Input,
) (int, wire.CommandInvokeResponse, bool) {
	action := in.Resolved.Action
	switch action.Kind {
	case contribution.ActionComposerPrefill:
		return http.StatusOK, wire.CommandInvokeResponse{
			Status:   "completed",
			UIEffect: &wire.CommandUIEffect{Kind: "composer_prefill", Text: action.Text},
		}, true
	case contribution.ActionNavigate:
		return http.StatusOK, wire.CommandInvokeResponse{
			Status:   "completed",
			UIEffect: &wire.CommandUIEffect{Kind: "navigate", Destination: action.Destination},
		}, true
	case contribution.ActionExternalLink:
		return http.StatusOK, wire.CommandInvokeResponse{
			Status:   "completed",
			UIEffect: &wire.CommandUIEffect{Kind: "external_link", URL: action.URL},
		}, true
	case contribution.ActionWorkflowStart:
		return s.executeWorkflowStart(w, r, scope, req, in)
	case contribution.ActionEditorAction:
		return s.ExecuteEditorAction(w, r, scope.sess, req, in)
	case contribution.ActionMCPTool:
		return s.executeMCPTool(w, r, scope, in)
	default:
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "action kind has no executor")
		return 0, wire.CommandInvokeResponse{}, false
	}
}

func (s *Handler) executeWorkflowStart(
	w http.ResponseWriter, r *http.Request,
	scope invokeScope, req wire.CommandInvokeRequest, in commandinvoke.Input,
) (int, wire.CommandInvokeResponse, bool) {
	workflowID := strings.TrimSpace(in.Resolved.Action.Workflow)
	manifests, _, err := workflowdef.LoadPackManifestsForCatalog(in.Frame.View.Catalog)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	version := ""
	for _, manifest := range workflowdef.NewRegistry(manifests).List() {
		if manifest.ID == workflowID && manifest.IsCatalogVisible() {
			version = manifest.Version
			break
		}
	}
	if err := s.Workflow.Workflows.Resolver.ValidateUserFacingStart(r.Context(), scope.projectDir, in.SessionID, workflowID, version); err != nil {
		s.Workflow.WriteWorkflowError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	// The invoke route carries human start authorization.
	run, err := s.Workflow.Workflows.Starts.Start(hostctx.WithHumanWorkflowStart(r.Context()), in.SessionID, wire.StartWorkflowRunRequest{
		OperationID:     req.OperationID,
		WorkflowID:      workflowID,
		WorkflowVersion: version,
	})
	if err != nil {
		s.Workflow.WriteWorkflowError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	return http.StatusAccepted, wire.CommandInvokeResponse{Status: "accepted", WorkflowRunID: run.ID}, true
}

// ExecuteEditorAction admits a prompt under its preset boundary.
func (s *Handler) ExecuteEditorAction(
	w http.ResponseWriter, r *http.Request,
	sess *wire.Session, req wire.CommandInvokeRequest, in commandinvoke.Input,
) (int, wire.CommandInvokeResponse, bool) {
	ref, err := contribution.ParseID(in.Resolved.Action.Ref)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	editorAction, ok := in.Frame.View.Contributions.EditorAction(ref)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "editor action "+ref.String()+" is not in the captured frame")
		return 0, wire.CommandInvokeResponse{}, false
	}
	boundary, ok := editorturn.PresetBoundaryFor(editorAction.Execution.Preset)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "preset "+string(editorAction.Execution.Preset)+" has no host implementation")
		return 0, wire.CommandInvokeResponse{}, false
	}
	if boundary.RequiresTarget && in.Context.Path == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "the editor action requires a structured target")
		return 0, wire.CommandInvokeResponse{}, false
	}
	prompt := in.Frame.PromptBody(editorAction.Execution.PromptRef)
	if len(prompt) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable, "prompt "+editorAction.Execution.PromptRef+" is not in the captured frame")
		return 0, wire.CommandInvokeResponse{}, false
	}

	// The preset fixes the tool profile and write pin.
	text, err := renderEditorPrompt(r.Context(), prompt, in.Context)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	input := promptinput.Input{Text: text, ToolProfile: boundary.ToolProfile}
	if in.Context.Path != "" {
		refPart := wire.PromptReferencePart{PathFile: &wire.PromptReferencePathFilePart{
			Kind:      wire.PromptReferencePathFile,
			ProjectID: in.ProjectID,
			RootID:    in.Context.RootID,
			Path:      in.Context.Path,
			StartLine: in.Context.StartLine,
			EndLine:   in.Context.EndLine,
		}}
		previewBudget := promptattach.NewTurnPreviewBudget(s.Prompt.Caps)
		refResult, err := promptattach.IngestReferences(s.Prompt.ReferenceDeps(r.Context(), sess), previewBudget, []wire.PromptReferencePart{refPart})
		if err != nil {
			s.Prompt.WriteAttachmentError(w, err)
			return 0, wire.CommandInvokeResponse{}, false
		}
		input.Text = promptattach.JoinUserText(text, refResult.Fences())
		input.ArtifactIDs = refResult.ArtifactIDs
		input.ContentParts = promptadmin.PromptContentParts(text, nil, refResult.Parts, nil)
		if pins := boundary.WritePins(in.Context.Path); len(pins) > 0 {
			input.WritePinRootID = in.Context.RootID
			input.WritePinGlobs = pins
		}
	}

	row, _, err := s.Sessions.Submissions.AdmitPrompt(r.Context(), in.SessionID, req.OperationID, req, input)
	if err != nil {
		if errors.Is(err, spendguard.ErrCeiling) {
			s.responses.FailDetails(w, wire.ApiErrorCodeSessionSpendCeilingReached, usernotice.SpendCeilingContext(err), "chat spend ceiling reached")
			return 0, wire.CommandInvokeResponse{}, false
		}
		var conflict *store.PromptSubmissionConflictError
		if errors.As(err, &conflict) {
			s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for a different prompt")
			return 0, wire.CommandInvokeResponse{}, false
		}
		s.responses.InternalError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	s.Prompt.ResumePromptSubmission(r.Context(), in.SessionID, row)
	return http.StatusAccepted, wire.CommandInvokeResponse{Status: "accepted", MessageID: row.ID}, true
}

func (s *Handler) executeMCPTool(
	w http.ResponseWriter, r *http.Request,
	scope invokeScope, in commandinvoke.Input,
) (int, wire.CommandInvokeResponse, bool) {
	requirementID, err := contribution.ParseID(in.Resolved.Action.Requirement)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return 0, wire.CommandInvokeResponse{}, false
	}
	status, ok := in.Frame.Requirement(requirementID)
	if !ok || !status.Ready {
		s.responses.Fail(w, wire.ApiErrorCodeCommandUnavailable,
			"MCP requirement "+requirementID.String()+" is not ready in the captured frame")
		return 0, wire.CommandInvokeResponse{}, false
	}
	requirement, _ := in.Frame.View.Contributions.MCPRequirement(requirementID)
	output, err := s.MCPRegistry.CallTool(r.Context(),
		mcp.ProjectScope(scope.projectID, scope.projectDir, scope.roots),
		requirement.ProviderID, in.Resolved.Action.Tool, in.Args)
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contributed MCP tool call failed", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeMcpCallFailed, "the MCP tool call failed")
		return 0, wire.CommandInvokeResponse{}, false
	}
	output, err = commandinvoke.PrepareOutput(in.Resolved.Output, output)
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contributed operation output rejected", "err", err)
		return OperationOutputFailure()
	}
	if in.Resolved.Result == contribution.ResultDiscard {
		output = ""
	}
	return http.StatusOK, wire.CommandInvokeResponse{Status: "completed", Output: output}, true
}

// OperationOutputFailure settles an operation whose output broke its declared
// shape; the receipt keeps host copy, never the provider's diagnostic.
func OperationOutputFailure() (int, wire.CommandInvokeResponse, bool) {
	return http.StatusBadGateway, wire.CommandInvokeResponse{
		Status: "failed",
		Error: &wire.CommandInvokeError{Code: wire.ApiErrorCodeOperationOutputInvalid,
			Message: "the operation returned output that does not match its declared shape"},
	}, true
}

// editorPromptVars is the closed prompt variable set.
func editorPromptVars(ctx wire.CommandInvokeContext) map[string]any {
	return map[string]any{
		"path":        strings.TrimSpace(ctx.Path),
		"root_id":     strings.TrimSpace(ctx.RootID),
		"start_line":  ctx.StartLine,
		"end_line":    ctx.EndLine,
		"symbol":      strings.TrimSpace(ctx.Symbol),
		"finding_id":  strings.TrimSpace(ctx.FindingID),
		"instruction": strings.TrimSpace(ctx.Instruction),
	}
}

// renderEditorPrompt renders captured prompt bytes.
func renderEditorPrompt(renderCtx context.Context, body []byte, ctx wire.CommandInvokeContext) (string, error) {
	tmpl, err := pongoplain.Compile(string(body))
	if err != nil {
		return "", fmt.Errorf("editor action prompt: %w", err)
	}
	out, err := pongoplain.Execute(renderCtx, tmpl, editorPromptVars(ctx))
	if err != nil {
		return "", fmt.Errorf("editor action prompt: %w", err)
	}
	text := strings.TrimSpace(out)
	if text == "" {
		return "", fmt.Errorf("editor action prompt rendered empty")
	}
	return text, nil
}
