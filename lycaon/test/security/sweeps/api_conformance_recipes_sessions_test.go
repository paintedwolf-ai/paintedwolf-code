package sweeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/secretmatch"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// sessionRecipes cover sessions, workflow runs, checkpoints, workers,
// delegations, transcripts, programs, background processes, artifacts and
// contribution commands.
var sessionRecipes = map[string]successRecipe{
	"startWorkflowRun":        {prepare: sessionPrepareStartRun},
	"getWorkflowRun":          {prepare: sessionPrepareGetRun},
	"getWorkflowRunReport":    {prepare: sessionPrepareRunReport},
	"listSessionWorkflowRuns": {seed: sessionSeedWorkflowRuns},
	"pauseWorkflowRun":        {prepare: sessionControlRecipe("")},
	"resumeWorkflowRun":       {prepare: sessionControlRecipe("pauseWorkflowRun")},
	"cancelWorkflowRun":       {prepare: sessionControlRecipe("")},
	"exitWorkflowRun":         {prepare: sessionControlRecipe("")},
	"advanceWorkflowRun":      {prepare: sessionControlRecipe("")},
	"fireWorkflowTransition":  {prepare: sessionPrepareTransition},
	"resolveWorkflowDecision": {prepare: sessionPrepareDecision},
	"resolveWorkflowFeedback": {prepare: sessionPrepareFeedback},
	"resolveWorkflowSecret":   {prepare: sessionPrepareWorkflowSecret},

	"composeWorkflow":             {prepare: sessionPrepareCompose},
	"composeWorkflowFromTemplate": {prepare: sessionPrepareComposeFromTemplate},
	"persistWorkflow":             {prepare: sessionPreparePersist},
	"approveBlueprint":            {prepare: sessionPrepareApproveBlueprint},

	"resolveCheckpoint":              {prepare: sessionPrepareResolveCheckpoint},
	"beginCheckpointUnlockChallenge": {prepare: sessionPrepareUnlockChallenge},

	"listWorkers":           {seed: sessionSeedWorkers},
	"cancelWorker":          {prepare: sessionPrepareCancelWorker},
	"getWorkerChanges":      {prepare: sessionPrepareWorkerChanges},
	"dispatchDelegationLeg": {prepare: sessionPrepareDispatchLeg},

	"previewSessionRewind":     {prepare: sessionPreparePreviewRewind},
	"rewindSession":            {prepare: sessionPrepareRewind},
	"resolveMessageNavigation": {prepare: sessionPrepareNavigation},
	"listDraftVersions":        {prepare: sessionPrepareDraftVersions},
	"updateSessionQueue":       {prepare: sessionPrepareQueueUpdate},
	"getCostSummary":           {prepare: sessionPrepareCostSummary},

	"getCommandProgramReceipt":     {prepare: sessionPrepareProgramReceipt},
	"getCommandReceiptDocument":    {prepare: sessionPrepareReceiptDocument},
	"searchCommandReceiptDocument": {prepare: sessionPrepareReceiptSearch},
	"listSessionInvocations":       {seed: sessionSeedInvocations},

	"getSessionBackgroundProcessOutput": {prepare: sessionPrepareProcessOutput},
	"stopBackgroundProcess":             {prepare: sessionPrepareStopProcess},
	"stopProjectBackgroundProcess":      {prepare: sessionPrepareStopProjectProcess},

	"createSessionArtifact": {prepare: sessionPrepareRecording},
	"getSessionArtifact":    {prepare: sessionPrepareGetArtifact},
	"deleteProjectArtifact": {prepare: sessionPrepareDeleteArtifact},
	"listProjectArtifacts":  {seed: sessionSeedProjectArtifacts},

	"invokeSessionCommand": {prepare: sessionPrepareInvokeSessionCommand},
	"invokeProjectCommand": {prepare: sessionPrepareInvokeProjectCommand},
	"landGitWorktree":      {prepare: sessionPrepareLandWorktree},
}

const sessionJSONType = "application/json"

// Fixture workflows registered on the host for run-control recipes.
const (
	sessionAdvanceWorkflow  = "conformance-advance"
	sessionFeedbackWorkflow = "conformance-feedback"
	sessionDecisionWorkflow = "conformance-decision"
	sessionReportWorkflow   = "conformance-report"
	sessionChoiceWorkflow   = "choice-transitions-fixture"
)

// sessionMemo keeps what a recipe made, so a read recipe that runs in every
// read pass makes its resource once per sweep.
var sessionMemo = struct {
	sync.Mutex
	values map[sessionMemoKey]any
}{values: map[sessionMemoKey]any{}}

type sessionMemoKey struct {
	sweep *conformanceSweep
	key   string
}

func sessionOnce[T any](s *conformanceSweep, key string, build func() (T, error)) (T, error) {
	sessionMemo.Lock()
	cached, ok := sessionMemo.values[sessionMemoKey{s, key}]
	sessionMemo.Unlock()
	if ok {
		return cached.(T), nil
	}
	value, err := build()
	if err != nil {
		return value, err
	}
	sessionMemo.Lock()
	sessionMemo.values[sessionMemoKey{s, key}] = value
	sessionMemo.Unlock()
	return value, nil
}

func sessionBody(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode recipe body: %v", err))
	}
	return raw
}

func sessionJSON(params map[string]string, body any) successInputs {
	return successInputs{params: params, body: sessionBody(body), contentType: sessionJSONType}
}

func sessionString(answer map[string]any, key string) string {
	value, _ := answer[key].(string)
	return value
}

func sessionInt(answer map[string]any, key string) int64 {
	value, _ := answer[key].(float64)
	return int64(value)
}

func sessionSettle(ctx context.Context, s *conformanceSweep) {
	s.host.Server.WaitForBackground(ctx)
}

func sessionSuffix() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

// sessionNewChat creates a chat in projectID and waits for its preparation.
func sessionNewChat(ctx context.Context, s *conformanceSweep, projectID, posture string) (string, error) {
	answer, err := s.prerequisite(ctx, "createSession", sessionJSON(nil, map[string]any{"project_id": projectID, "posture": posture}))
	if err != nil {
		return "", err
	}
	id := sessionString(answer, "id")
	if id == "" {
		return "", errors.New("createSession answered no id")
	}
	sessionSettle(ctx, s)
	return id, nil
}

// sessionWorkflowChat is the chat whose runs the run-control recipes replace.
func sessionWorkflowChat(ctx context.Context, s *conformanceSweep) (string, error) {
	return sessionOnce(s, "workflow-chat", func() (string, error) {
		return sessionNewChat(ctx, s, s.fixture.project.ID, string(wire.SessionPostureBuild))
	})
}

// sessionRegisterManifests adds the fixture workflows to the host catalog.
func sessionRegisterManifests(s *conformanceSweep) error {
	_, err := sessionOnce(s, "manifests", func() (bool, error) {
		twoPhases := []workflowdef.PhaseDef{{ID: "draft", Next: "done"}, {ID: "done"}}
		s.host.RegisterManifest(workflowdef.Manifest{ID: sessionAdvanceWorkflow, Version: "1.0.0", PhaseDefs: twoPhases})
		s.host.RegisterManifest(workflowdef.Manifest{
			ID: sessionReportWorkflow, Version: "1.0.0", PhaseDefs: twoPhases,
			Controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: true}},
		})
		s.host.RegisterManifest(workflowdef.Manifest{
			ID: sessionFeedbackWorkflow, Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{
				ID: "clarify", CompleteWhen: "user_feedback_received:clarify",
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto, Next: "done",
				OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "REST or GraphQL?"}},
			}, {ID: "done"}},
		})
		s.host.RegisterManifest(workflowdef.Manifest{
			ID: sessionDecisionWorkflow, Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{
				ID: "confirm", CompleteWhen: "user_decision:confirm,yes",
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
				OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt: "Proceed?", ResponseType: workflowdef.FeedbackResponseSingleChoice, Options: []string{"yes", "no"},
				}},
			}},
		})
		raw, err := os.ReadFile(sessionRepoFile("lycaon", "config", "fixtures", "workflows", "choice-transitions.yaml"))
		if err != nil {
			return false, err
		}
		choice, err := workflowdef.ParseManifestYAML(raw)
		if err != nil {
			return false, err
		}
		s.host.RegisterManifest(choice)
		return true, nil
	})
	return err
}

// sessionRun is a started workflow run at its settled revision.
type sessionRun struct {
	id, session string
	revision    int64
}

func sessionRunRevision(ctx context.Context, s *conformanceSweep, runID string) (int64, error) {
	answer, err := s.prerequisite(ctx, "getWorkflowRun", successInputs{params: map[string]string{"id": runID}})
	if err != nil {
		return 0, err
	}
	return sessionInt(answer, "revision"), nil
}

// sessionStartBody is a start request that replaces the chat's active run.
func sessionStartBody(ctx context.Context, s *conformanceSweep, chat string, fields map[string]any) (map[string]any, error) {
	body := map[string]any{"operation_id": uuid.NewString()}
	for name, value := range fields {
		body[name] = value
	}
	answer, err := s.prerequisite(ctx, "getActiveWorkflowRun", successInputs{params: map[string]string{"id": chat}})
	if err != nil {
		return nil, err
	}
	if active, ok := answer["run"].(map[string]any); ok {
		body["replace_run_id"] = sessionString(active, "id")
		body["expected_revision"] = sessionInt(active, "revision")
	}
	return body, nil
}

func sessionStartRun(ctx context.Context, s *conformanceSweep, chat string, fields map[string]any) (sessionRun, error) {
	sessionSettle(ctx, s)
	body, err := sessionStartBody(ctx, s, chat, fields)
	if err != nil {
		return sessionRun{}, err
	}
	answer, err := s.prerequisite(ctx, "startWorkflowRun", sessionJSON(map[string]string{"id": chat}, body))
	if err != nil {
		return sessionRun{}, err
	}
	run := sessionRun{id: sessionString(answer, "id"), session: chat}
	// Start hooks may stamp the run after answering.
	sessionSettle(ctx, s)
	run.revision, err = sessionRunRevision(ctx, s, run.id)
	return run, err
}

// sessionStartFixtureRun starts a fixture workflow on the workflow chat.
func sessionStartFixtureRun(ctx context.Context, s *conformanceSweep, workflowID string, extra map[string]any) (sessionRun, error) {
	if err := sessionRegisterManifests(s); err != nil {
		return sessionRun{}, err
	}
	chat, err := sessionWorkflowChat(ctx, s)
	if err != nil {
		return sessionRun{}, err
	}
	fields := map[string]any{"workflow_id": workflowID, "workflow_version": "1.0.0"}
	for name, value := range extra {
		fields[name] = value
	}
	return sessionStartRun(ctx, s, chat, fields)
}

func sessionPrepareStartRun(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionWorkflowChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	sessionSettle(ctx, s)
	body, err := sessionStartBody(ctx, s, chat, map[string]any{
		"workflow_id": "plan", "workflow_version": "1.0.1", "request": "Plan the fixture change",
	})
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat}, body), nil
}

func sessionPrepareGetRun(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	run, err := sessionOnce(s, "read-run", func() (sessionRun, error) {
		return sessionStartFixtureRun(ctx, s, sessionAdvanceWorkflow, nil)
	})
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": run.id}}, nil
}

// sessionPrepareRunReport ends a report-enabled run that delivered a grounded
// completion report.
func sessionPrepareRunReport(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	runID, err := sessionOnce(s, "report-run", func() (string, error) {
		run, err := sessionStartFixtureRun(ctx, s, sessionReportWorkflow, nil)
		if err != nil {
			return "", err
		}
		if _, err := s.host.Workflows.Manager.Vars.Stamp(ctx, run.id, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			return runstate.SetGateSatisfied(vars, "topology_report_delivered", true), true, nil
		}); err != nil {
			return "", err
		}
		if err := s.host.Store.AppendMessages(ctx, run.session, wire.Message{
			ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel,
			Kind: wire.MessageKindCompletionReport, Content: "The fixture change is complete.",
			CompletionReport: &wire.CompletionReportMeta{Scope: wire.CompletionReportScopeRun, Phase: "done", Headline: "Fixture change complete"},
			Visibility:       wire.MessageVisibilityTranscript, WorkflowRunID: run.id,
			Grounding: &wire.CitationGrounding{Traced: true},
		}); err != nil {
			return "", err
		}
		revision, err := sessionRunRevision(ctx, s, run.id)
		if err != nil {
			return "", err
		}
		if _, err := s.prerequisite(ctx, "cancelWorkflowRun", sessionJSON(map[string]string{"id": run.id}, map[string]any{"expected_revision": revision})); err != nil {
			return "", err
		}
		return run.id, nil
	})
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": runID}}, nil
}

// sessionControlRecipe sends a control to a fresh run; before, when set, is
// the control that first moves the run into the state the control acts on.
func sessionControlRecipe(before string) func(context.Context, *conformanceSweep, *conformanceOp) (successInputs, error) {
	return func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		run, err := sessionStartFixtureRun(ctx, s, sessionAdvanceWorkflow, nil)
		if err != nil {
			return successInputs{}, err
		}
		if before != "" {
			answer, err := s.prerequisite(ctx, before, sessionJSON(map[string]string{"id": run.id}, map[string]any{"expected_revision": run.revision}))
			if err != nil {
				return successInputs{}, err
			}
			run.revision = sessionInt(answer, "revision")
		}
		return sessionJSON(map[string]string{"id": run.id}, map[string]any{"expected_revision": run.revision}), nil
	}
}

func sessionPrepareTransition(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	run, err := sessionStartFixtureRun(ctx, s, sessionChoiceWorkflow, map[string]any{"request": "Review the fixture and choose the next step."})
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": run.id, "transition_id": "critique"}, map[string]any{"expected_revision": run.revision}), nil
}

func sessionPrepareDecision(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	run, err := sessionStartFixtureRun(ctx, s, sessionDecisionWorkflow, nil)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": run.id, "phase_id": "confirm"}, map[string]any{"expected_revision": run.revision, "choice": "yes"}), nil
}

func sessionPrepareFeedback(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	run, err := sessionStartFixtureRun(ctx, s, sessionFeedbackWorkflow, nil)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": run.id, "phase_id": "clarify"}, map[string]any{"expected_revision": run.revision, "response": "Use REST"}), nil
}

// sessionPrepareWorkflowSecret opens a secret ask the way ask_user does and
// answers it with a sentinel the sweep scans every response for.
func sessionPrepareWorkflowSecret(ctx context.Context, s *conformanceSweep, op *conformanceOp) (successInputs, error) {
	run, err := sessionStartFixtureRun(ctx, s, sessionAdvanceWorkflow, nil)
	if err != nil {
		return successInputs{}, err
	}
	ask, err := s.host.Workflows.Manager.Asks.RequestUserInput(s.host.OwnerCtx(s.fixture.t, ctx), run.session, workflowinputs.UserInputRequest{
		Prompt: "Provide the registry key", ResponseType: workflowdef.FeedbackResponseSecret, ToolCallID: "conformance-" + sessionSuffix(),
		Secret: &workflowdef.SecretInputSpec{Name: "Registry key", Purpose: "Publish the fixture", Scope: "chat"},
	})
	if err != nil {
		return successInputs{}, err
	}
	revision, err := sessionRunRevision(ctx, s, run.id)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": run.id, "phase_id": ask.PhaseID}, map[string]any{
		"expected_revision": revision, "secret_value": s.secrets.issue(op.ID + ".secret_value"),
	}), nil
}

// sessionSeedWorkflowRuns gives the fixture chat two runs beside its
// ambient one, then exits the last so the chat returns to ordinary work.
func sessionSeedWorkflowRuns(ctx context.Context, s *conformanceSweep) error {
	if err := sessionRegisterManifests(s); err != nil {
		return err
	}
	var last sessionRun
	for range 2 {
		run, err := sessionStartRun(ctx, s, s.fixture.session.ID, map[string]any{"workflow_id": sessionAdvanceWorkflow, "workflow_version": "1.0.0"})
		if err != nil {
			return err
		}
		last = run
	}
	_, err := s.prerequisite(ctx, "exitWorkflowRun", sessionJSON(map[string]string{"id": last.id}, map[string]any{"expected_revision": last.revision}))
	return err
}

// sessionComposeYAML is the session workflow fixture under a fresh id.
func sessionComposeYAML(id string) ([]byte, error) {
	raw, err := os.ReadFile(sessionRepoFile("lycaon", "test", "contract", "testdata", "workflow", "compose_session_plan.yaml"))
	if err != nil {
		return nil, err
	}
	const fixtureID = "id: hotfix-session\n"
	if !strings.HasPrefix(string(raw), fixtureID) {
		return nil, errors.New("compose fixture no longer starts with its id")
	}
	return []byte("id: " + id + "\n" + strings.TrimPrefix(string(raw), fixtureID)), nil
}

func sessionPrepareCompose(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	body, err := sessionComposeYAML("conformance-compose-" + sessionSuffix())
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID}, body: body, contentType: "application/yaml"}, nil
}

func sessionTemplateBody(workflowID string) map[string]any {
	return map[string]any{"template_id": "hotfix-template", "params": map[string]any{"workflow_id": workflowID}}
}

func sessionPrepareComposeFromTemplate(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return sessionJSON(map[string]string{"id": s.fixture.session.ID}, sessionTemplateBody("conformance-template-"+sessionSuffix())), nil
}

// sessionPreparePersist composes a chat workflow and saves it under its own
// trigger; the inherited one collides with the bundled plan.
func sessionPreparePersist(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id := "conformance-persist-" + sessionSuffix()
	chat := s.fixture.session.ID
	if _, err := s.prerequisite(ctx, "composeWorkflowFromTemplate", sessionJSON(map[string]string{"id": chat}, sessionTemplateBody(id))); err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat, "workflow_id": id}, map[string]any{"version": "1.0.0", "confirm": true, "trigger": "/" + id}), nil
}

// sessionPrepareApproveBlueprint drives a plan run to its approval phase with
// a valid plan, then approves that plan at the run's revision.
func sessionPrepareApproveBlueprint(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionNewChat(ctx, s, s.fixture.project.ID, string(wire.SessionPostureBuild))
	if err != nil {
		return successInputs{}, err
	}
	run, err := sessionStartRun(ctx, s, chat, map[string]any{"workflow_id": "plan", "workflow_version": "1.0.1", "request": "Plan the fixture change"})
	if err != nil {
		return successInputs{}, err
	}
	if err := sessionDrivePlanToApproval(ctx, s, run.id); err != nil {
		return successInputs{}, err
	}
	current, err := s.host.Workflows.Manager.Store.Runs.Get(ctx, run.id)
	if err != nil {
		return successInputs{}, err
	}
	plan, err := s.host.Workflows.Blueprints.Get(ctx, current.ProjectID, current.BlueprintPath)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": current.ProjectID, "blueprint_id": plan.ID}, map[string]any{
		"workflow_run_id": current.ID, "expected_revision": current.Revision,
		"content_digest": workflowdef.HashBlueprintContent(plan.Content),
	}), nil
}

// sessionDrivePlanToApproval satisfies each plan phase the way a person and
// the agents would, advancing through the route, until approval is pending.
func sessionDrivePlanToApproval(ctx context.Context, s *conformanceSweep, runID string) error {
	workflows := s.host.Workflows.Manager
	for range 8 {
		sessionSettle(ctx, s)
		run, err := workflows.Store.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		vars, err := workflows.Store.Runs.GetScaffoldVars(ctx, runID)
		if err != nil {
			return err
		}
		switch phase := run.CurrentPhase; phase {
		case "approve":
			return nil
		case "intake":
			vars["hitl_consulted:intake"] = true
		case "research", "review":
			vars = runstate.SetHostVar(vars, "params."+phase+"_depth", "none")
			vars = runstate.ResolveDepthSkip(vars, phase, phase+"_depth")
			if phase == "review" {
				vars = runstate.SetGateSatisfied(vars, "evidence_passed:plan_review", true)
			}
		case "expand":
			plan, err := s.host.Workflows.Blueprints.Get(ctx, run.ProjectID, run.BlueprintPath)
			if err != nil {
				return err
			}
			if _, err := s.host.Workflows.Blueprints.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, conditions.TestPlanContentWithTasks, blueprint.ContentDigest(plan.Content)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("plan run rests in phase %q", phase)
		}
		if err := workflows.Store.State.UpdateVars(ctx, run, s.fixture.projectDir, vars); err != nil {
			return err
		}
		revision, err := sessionRunRevision(ctx, s, runID)
		if err != nil {
			return err
		}
		if _, err := s.prerequisite(ctx, "advanceWorkflowRun", sessionJSON(map[string]string{"id": runID}, map[string]any{"expected_revision": revision})); err != nil {
			return err
		}
	}
	return errors.New("plan run did not reach approval")
}

// sessionPrepareResolveCheckpoint opens a content_apply card and rejects it,
// so nothing is written.
func sessionPrepareResolveCheckpoint(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	before := "conformance\n"
	resp, err := s.host.Sessions.Checkpoints.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: s.fixture.session.ID, Kind: wire.CheckpointKindContentApply,
		ContentApply: &hitl.ContentApplyPayload{Tool: "edit", ToolCallID: uuid.NewString(), Path: "README.md", Before: &before, After: "changed\n"},
	})
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": s.fixture.session.ID, "checkpoint_id": resp.CheckpointID},
		map[string]any{"kind": string(wire.CheckpointKindContentApply), "decision": string(wire.ContentApplyReject)}), nil
}

// sessionPrepareUnlockChallenge opens the card that would release a value a
// person holds while its chat is locked, and asks presence for its unlock
// option. Presence answers only when the host holds a verification key.
func sessionPrepareUnlockChallenge(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionNewChat(ctx, s, s.fixture.project.ID, string(wire.SessionPostureBuild))
	if err != nil {
		return successInputs{}, err
	}
	recipients := []secretmatch.Recipient{{
		ID: "https://api.example.invalid:443", Label: "https://api.example.invalid",
		Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService,
	}}
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "http_request"}, Scope: hitl.ActionScope{SessionID: chat}}
	held := hitl.HeldRelease{
		ProjectID: s.fixture.project.ID, ChatSessionID: chat, Recipients: recipients,
		Secrets: []hitl.HeldSecret{{SecretID: uuid.NewString(), Version: 1, Name: "Deploy key"}},
	}
	plan, err := hitl.NewUnlockPlan(action, held, &hitl.SecretScreen{
		Surface: "http_request", SurfaceLabel: "HTTP request", CanRedact: true, Managed: true, Recipients: recipients,
	})
	if err != nil {
		return successInputs{}, err
	}
	resp, err := s.host.Sessions.Checkpoints.RequestCheckpoint(s.host.OwnerCtx(s.fixture.t, ctx), hitl.CheckpointRequest{
		SessionID: chat, Kind: wire.CheckpointKindToolApproval, Title: hitl.TitleUnlockForThisChat,
		ProposedAction: &action, ApprovalPlan: plan,
	})
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat, "checkpoint_id": resp.CheckpointID},
		map[string]any{"option_id": "unlock_for_chat", "window_label": "Conformance"}), nil
}

// sessionEnqueueWorker queues a read worker under chat. The sweep runs no
// worker claimers, so it stays pending until canceled.
func sessionEnqueueWorker(ctx context.Context, s *conformanceSweep, chat string) (string, error) {
	if len(s.fixture.project.Roots) == 0 {
		return "", errors.New("the fixture project has no root")
	}
	return s.host.Workers.Queue.Enqueue(ctx, wire.WorkerTask{
		Prompt: "conformance", Brief: "conformance", ParentSessionID: chat, ProjectID: s.fixture.project.ID,
		WorkspaceRootID: s.fixture.project.Roots[0].ID, WorkspacePath: s.fixture.projectDir, AgentType: "implementer",
		Scope: &wire.TaskScope{Mode: wire.TaskScopeModeRead},
	})
}

// sessionSettledWorker queues a worker and cancels it, so no pending worker
// holds a chat that later probes prompt.
func sessionSettledWorker(ctx context.Context, s *conformanceSweep, chat string) (string, error) {
	id, err := sessionEnqueueWorker(ctx, s, chat)
	if err != nil {
		return "", err
	}
	return id, s.host.Workers.Queue.Cancel(ctx, id, nil)
}

func sessionSeedWorkers(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		if _, err := sessionSettledWorker(ctx, s, s.fixture.session.ID); err != nil {
			return err
		}
	}
	return nil
}

func sessionPrepareCancelWorker(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionWorkflowChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	id, err := sessionEnqueueWorker(ctx, s, chat)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": id}}, nil
}

func sessionPrepareWorkerChanges(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := sessionOnce(s, "changes-worker", func() (string, error) {
		chat, err := sessionWorkflowChat(ctx, s)
		if err != nil {
			return "", err
		}
		return sessionSettledWorker(ctx, s, chat)
	})
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": id}}, nil
}

// sessionPrepareDispatchLeg dispatches the pending leg of a fresh delegation;
// an earlier probe may have aborted the fixture one.
func sessionPrepareDispatchLeg(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	answer, err := s.prerequisite(ctx, "createDelegation", sessionJSON(nil, map[string]any{
		"operation_id": uuid.NewString(), "project_id": s.fixture.project.ID, "task": "Review the fixture README.",
	}))
	if err != nil {
		return successInputs{}, err
	}
	id := sessionString(answer, "id")
	if id == "" {
		return successInputs{}, errors.New("createDelegation answered no id")
	}
	return sessionJSON(map[string]string{"id": id}, map[string]any{}), nil
}

// sessionPrompt sends a prompt to chat and waits for its turn; the prompt's
// operation id names its user message.
func sessionPrompt(ctx context.Context, s *conformanceSweep, chat, text string) (string, error) {
	sessionSettle(ctx, s)
	id := uuid.NewString()
	if _, err := s.prerequisite(ctx, "sendPrompt", sessionJSON(map[string]string{"id": chat}, map[string]any{"operation_id": id, "text": text})); err != nil {
		return "", err
	}
	sessionSettle(ctx, s)
	return id, nil
}

// sessionPromptedChat is a fresh chat with one answered prompt to anchor a
// rewind.
func sessionPromptedChat(ctx context.Context, s *conformanceSweep) (string, string, error) {
	chat, err := sessionNewChat(ctx, s, s.fixture.project.ID, string(wire.SessionPostureSpec))
	if err != nil {
		return "", "", err
	}
	anchor, err := sessionPrompt(ctx, s, chat, "change the note")
	return chat, anchor, err
}

func sessionPreparePreviewRewind(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, anchor, err := sessionPromptedChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat}, map[string]any{"message_id": anchor}), nil
}

func sessionPrepareRewind(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, anchor, err := sessionPromptedChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	preview, err := s.prerequisite(ctx, "previewSessionRewind", sessionJSON(map[string]string{"id": chat}, map[string]any{"message_id": anchor}))
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat}, map[string]any{
		"operation_id": uuid.NewString(), "message_id": anchor, "plan_digest": sessionString(preview, "plan_digest"),
	}), nil
}

// sessionAssistantMessage is a visible assistant reply in the fixture chat.
type sessionAssistantMessage struct{ id, content string }

func sessionReply(ctx context.Context, s *conformanceSweep) (sessionAssistantMessage, error) {
	return sessionOnce(s, "assistant-reply", func() (sessionAssistantMessage, error) {
		msg := sessionAssistantMessage{id: uuid.NewString(), content: "The fixture is described in README.md."}
		err := s.host.Store.AppendMessages(ctx, s.fixture.session.ID, wire.Message{
			ID: msg.id, Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel,
			Content: msg.content, Visibility: wire.MessageVisibilityTranscript,
		})
		return msg, err
	})
}

func sessionPrepareNavigation(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	msg, err := sessionReply(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": s.fixture.session.ID}, map[string]any{
		"message_id": msg.id, "content_sha256": sessionstore.NavigationContentHash(msg.content),
	}), nil
}

// sessionPrepareDraftVersions names a coordinator reply as the draft slot.
func sessionPrepareDraftVersions(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	msg, err := sessionReply(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID, "slot_id": msg.id}}, nil
}

func sessionPrepareQueueUpdate(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat := s.fixture.session.ID
	queue, err := s.prerequisite(ctx, "getSessionQueue", successInputs{params: map[string]string{"id": chat}})
	if err != nil {
		return successInputs{}, err
	}
	return sessionJSON(map[string]string{"id": chat}, map[string]any{
		"op": "hold", "hold": false, "expected_revision": sessionInt(queue, "revision"),
	}), nil
}

// sessionPrepareCostSummary selects the session scope; the handler requires
// exactly one of session_id or project_id.
func sessionPrepareCostSummary(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return successInputs{query: map[string]string{"session_id": s.fixture.session.ID}}, nil
}

// sessionProgram is a settled command program in the fixture chat and the
// revision of its receipt document.
type sessionProgram struct{ id, revision string }

// sessionFixtureProgram runs the mock model's readme prompt, whose command
// program the receipt reads address.
func sessionFixtureProgram(ctx context.Context, s *conformanceSweep) (sessionProgram, error) {
	return sessionOnce(s, "program", func() (sessionProgram, error) {
		chat := s.fixture.session.ID
		if _, err := sessionPrompt(ctx, s, chat, "read the readme"); err != nil {
			return sessionProgram{}, err
		}
		var program sessionProgram
		if err := s.host.DB.QueryRowContext(ctx, `SELECT p.id FROM invocation_programs p
			JOIN invocation_receipts r ON r.id = p.parent_receipt_id
			WHERE r.session_id = ? ORDER BY p.started_at DESC LIMIT 1`, chat).Scan(&program.id); err != nil {
			return sessionProgram{}, fmt.Errorf("find the readme command program: %w", err)
		}
		document, err := s.prerequisite(ctx, "getCommandReceiptDocument", successInputs{
			params: map[string]string{"id": chat}, query: map[string]string{"scopes": program.id},
		})
		if err != nil {
			return sessionProgram{}, err
		}
		program.revision = sessionString(document, "revision")
		return program, nil
	})
}

func sessionPrepareProgramReceipt(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	program, err := sessionFixtureProgram(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID, "program_id": program.id}}, nil
}

func sessionPrepareReceiptDocument(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	program, err := sessionFixtureProgram(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID}, query: map[string]string{"scopes": program.id}}, nil
}

func sessionPrepareReceiptSearch(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	program, err := sessionFixtureProgram(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID}, query: map[string]string{
		// Every receipt row carries a type field, so a search walks many matches.
		"scopes": program.id, "revision": program.revision, "q": `"type"`,
	}}, nil
}

// sessionSeedInvocations answers two readme prompts, each a command
// invocation in the fixture chat.
func sessionSeedInvocations(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		if _, err := sessionPrompt(ctx, s, s.fixture.session.ID, "read the readme"); err != nil {
			return err
		}
	}
	return nil
}

func sessionBackgroundChat(ctx context.Context, s *conformanceSweep) (string, error) {
	return sessionOnce(s, "background-chat", func() (string, error) {
		return sessionNewChat(ctx, s, s.fixture.project.ID, string(wire.SessionPostureBuild))
	})
}

// sessionStartProcess starts a long-running background process in chat, as
// the command tool does for a background job.
func sessionStartProcess(ctx context.Context, s *conformanceSweep, chat string) (string, error) {
	return s.host.Sessions.Manager.Processes.Background.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: chat, ProjectID: s.fixture.project.ID, Runner: hostcmd.NewRunner(), Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch: exec.HostLaunch("conformance-server"), ProjectDir: s.fixture.projectDir,
			Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
}

func sessionPrepareProcessOutput(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionBackgroundChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	handle, err := sessionOnce(s, "output-process", func() (string, error) { return sessionStartProcess(ctx, s, chat) })
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": chat, "process_id": handle}}, nil
}

func sessionPrepareStopProcess(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionBackgroundChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	handle, err := sessionStartProcess(ctx, s, chat)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": chat, "process_id": handle}}, nil
}

func sessionPrepareStopProjectProcess(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat, err := sessionBackgroundChat(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	handle, err := sessionStartProcess(ctx, s, chat)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.project.ID, "process_id": handle}}, nil
}

// sessionPrepareRecording uploads a page-tool recording for a fresh tool call
// in the fixture chat; the call is answered so the transcript stays whole.
func sessionPrepareRecording(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	chat := s.fixture.session.ID
	messageID, callID := uuid.NewString(), "conformance-call-"+sessionSuffix()
	if err := s.host.Store.AppendMessages(ctx, chat,
		wire.Message{
			ID: messageID, Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel,
			ToolCalls: []wire.ToolCall{{ID: callID, Name: "page_act", Args: map[string]any{}}},
		},
		wire.Message{
			ID: uuid.NewString(), Role: wire.MessageRoleTool, Content: "recorded", Visibility: wire.MessageVisibilityTranscript,
			ToolResult: &wire.ToolResult{Tool: "page_act", ToolCallID: callID, Content: "ok", Outcome: wire.ToolResultOutcomeCompleted},
		},
	); err != nil {
		return successInputs{}, err
	}
	return successInputs{
		params: map[string]string{"id": chat},
		query: map[string]string{
			"page_id": "conformance-page", "assistant_message_id": messageID, "tool_call_id": callID, "operation_id": uuid.NewString(),
		},
		body: []byte("conformance recording"), contentType: "video/mp4",
	}, nil
}

func sessionRecordingID(ctx context.Context, s *conformanceSweep) (string, error) {
	in, err := sessionPrepareRecording(ctx, s, nil)
	if err != nil {
		return "", err
	}
	answer, err := s.prerequisite(ctx, "createSessionArtifact", in)
	if err != nil {
		return "", err
	}
	id := sessionString(answer, "id")
	if id == "" {
		return "", errors.New("createSessionArtifact answered no id")
	}
	return id, nil
}

func sessionPrepareGetArtifact(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := sessionOnce(s, "read-artifact", func() (string, error) { return sessionRecordingID(ctx, s) })
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.session.ID, "artifact_id": id}}, nil
}

func sessionPrepareDeleteArtifact(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := sessionRecordingID(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": s.fixture.project.ID, "artifact_id": id}}, nil
}

func sessionSeedProjectArtifacts(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		if _, err := sessionRecordingID(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// sessionPrepareInvokeSessionCommand invokes the bundled summarize editor
// action on the fixture README.
func sessionPrepareInvokeSessionCommand(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	frame, err := s.host.Server.Admin.Extensions.Contributions.CaptureContributionFrame(ctx, s.fixture.project.ID, s.fixture.projectDir)
	if err != nil {
		return successInputs{}, err
	}
	if len(s.fixture.project.Roots) == 0 {
		return successInputs{}, errors.New("the fixture project has no root")
	}
	for _, command := range frame.View.Contributions.Commands() {
		resolved, ok := frame.View.Contributions.ResolveCommand(command)
		if !ok || resolved.Action.Kind != contribution.ActionEditorAction || !strings.HasSuffix(command.ID, ":editor-verb-summarize") {
			continue
		}
		return sessionJSON(map[string]string{"id": s.fixture.session.ID, "command_id": command.ID}, map[string]any{
			"operation_id": uuid.NewString(), "frame_revision": frame.Revision,
			"context": map[string]any{"path": "README.md", "root_id": s.fixture.project.Roots[0].ID, "start_line": 1, "end_line": 1},
		}), nil
	}
	return successInputs{}, errors.New("the contribution frame has no summarize editor action")
}

// sessionPrepareInvokeProjectCommand invokes a project-scoped command of the
// device catalog. Bundled packs contribute none.
func sessionPrepareInvokeProjectCommand(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	frame, err := s.host.Server.Admin.Extensions.Contributions.CaptureContributionFrame(ctx, s.fixture.project.ID, s.fixture.projectDir)
	if err != nil {
		return successInputs{}, err
	}
	for _, command := range frame.View.Contributions.Commands() {
		resolved, ok := frame.View.Contributions.ResolveCommand(command)
		if !ok || contribution.InvocationScopeFor(resolved.Action.Kind) != contribution.InvocationProject {
			continue
		}
		return sessionJSON(map[string]string{"id": s.fixture.project.ID, "command_id": command.ID}, map[string]any{
			"operation_id": uuid.NewString(), "frame_revision": frame.Revision,
		}), nil
	}
	return successInputs{}, errors.New("the contribution frame has no project-scoped command")
}

// sessionPrepareLandWorktree binds a chat in a committed repository project
// to a worktree; landing it has nothing to land.
func sessionPrepareLandWorktree(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	t := s.fixture.t
	dir := s.host.ProjectDir(t, "conformance-worktree-"+sessionSuffix())
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("worktree\n"), 0o600); err != nil {
		return successInputs{}, err
	}
	gittest.InitCommit(t, dir, "init")
	created, err := s.prerequisite(ctx, "createProject", sessionJSON(nil, map[string]any{"roots": []any{map[string]any{"path": dir}}}))
	if err != nil {
		return successInputs{}, err
	}
	projectID := sessionString(created, "id")
	chat, err := sessionNewChat(ctx, s, projectID, string(wire.SessionPostureBuild))
	if err != nil {
		return successInputs{}, err
	}
	repos, err := s.prerequisite(ctx, "listGitRepos", successInputs{params: map[string]string{"id": projectID}})
	if err != nil {
		return successInputs{}, err
	}
	repoID := sessionString(repos, "active_repo_id")
	if repoID == "" {
		return successInputs{}, errors.New("the worktree project has no active repository")
	}
	if _, err := s.prerequisite(ctx, "bindGitWorktree", sessionJSON(map[string]string{"id": chat}, map[string]any{
		"repo_id": repoID, "branch": "session/" + chat,
	})); err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"id": chat}}, nil
}

// sessionRepoFile resolves a path from the repository root.
func sessionRepoFile(parts ...string) string {
	root, err := openapi.RepoRoot()
	if err != nil {
		return filepath.Join(parts...)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}
