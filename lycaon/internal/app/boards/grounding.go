package boards

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/decisions"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workermessaging"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerroster"
)

// WireGroundingAndFindings wires grounding coordinators, progress, visual, and decision tools.
func (r *Runtime) WireGroundingAndFindings(ctx context.Context) error {
	r.deps.Sessions.Manager.Coordinator.Guards.SetRules(r.deps.Workflows.Rules)
	if err := r.wireGroundingCoordinators(); err != nil {
		return err
	}
	if err := r.wireFindingAndProgressTools(); err != nil {
		return err
	}
	if err := r.wireVisualAndRenderTools(ctx); err != nil {
		return err
	}
	if err := r.wireDecisionAndCallTools(); err != nil {
		return err
	}
	r.wireApprovalRationaleAttacher()
	return nil
}

func (r *Runtime) wireGroundingCoordinators() error {
	groundingCfg, err := delegation.LoadGroundingConfig()
	if err != nil {
		return fmt.Errorf("grounding config: %w", err)
	}
	groundingGate := delegation.NewSimpleDelegationGroundingGate(groundingCfg)
	groundingGate.Criteria = r.deps.Delegations.Criteria
	groundingState := grounding.NewStateStore()
	r.deps.Delegations.Manager.Grounding = delegation.NewGroundingCoordinator(r.deps.Delegations.Store, r.deps.Delegations.Queue, groundingGate, groundingCfg, groundingState, r.deps.Sessions.Manager)
	r.deps.Delegations.Manager.Grounding.InspectorCloseout = r.deps.Delegations.Manager.InspectorCloseout
	r.deps.Delegations.Manager.Grounding.Events = r.deps.Events.Publisher
	r.deps.Delegations.Manager.Grounding.Pipeline = r.deps.Sessions.Manager.ToolPolicy.Pipeline
	ambientGate := delegation.NewSimpleAmbientGroundingGate(groundingCfg)
	ambientState := grounding.NewStateStore()
	ambientCoord := delegation.NewAmbientGroundingCoordinator(r.deps.Delegations.Store, r.deps.Delegations.Queue, ambientGate, groundingCfg, ambientState, r.deps.Sessions.Manager)
	ambientCoord.Events = r.deps.Events.Publisher
	ambientCoord.Pipeline = r.deps.Sessions.Manager.ToolPolicy.Pipeline
	r.deps.Sessions.Manager.SetGroundingHook(&delegation.ChainedGroundingCoordinator{
		Delegation: r.deps.Delegations.Manager.Grounding,
		Ambient:    ambientCoord,
	})
	r.Grounding = &toolhost.GroundingService{
		Config:    groundingCfg,
		State:     grounding.NewStateStore(),
		Ledger:    r.deps.Storage.Sessions,
		RejectFmt: r.deps.Execution.Rejections,
		Nudger:    r.deps.Sessions.Manager.Coordinator.Guidance,
	}
	return nil
}

func (r *Runtime) wireFindingAndProgressTools() error {
	r.Findings = findings.NewSQLStore(r.deps.Storage.Database)
	r.deps.Sessions.Manager.Workers.Notes.SetFindings(r.Findings)
	r.deps.Sessions.Manager.SetPeerRejectionFeed(workeroutcomes.NewPeerRejectionFeed())
	r.WorkerRecords = workerrecords.New(r.deps.Storage.Database)
	r.deps.Sessions.Manager.SetWorkerMessages(&workermessaging.Delivery{Records: r.WorkerRecords})
	r.deps.Sessions.Manager.Coordinator.ProgressClosure.SetAccounts(r.WorkerRecords)
	r.deps.Sessions.Manager.SetWorkerNames(r.WorkerRecords.NameByJob)
	if err := native.RegisterMessageCoordinatorTool(r.deps.Execution.Host.Registry, workertools.MessageCoordinatorDeps{
		Records:    r.WorkerRecords,
		NeedsReply: r.deps.Sessions.Manager.Coordinator.Workers.MessageNeedsReply,
	}); err != nil {
		return fmt.Errorf("message_coordinator tool: %w", err)
	}
	if err := native.RegisterRecordFindingTool(r.deps.Execution.Host.Registry, reporttools.RecordFindingGates{
		Grounding: r.Grounding,
	}, r.Findings, r.rootSessionKey); err != nil {
		return fmt.Errorf("record_finding tool: %w", err)
	}
	r.Progress = progress.NewSQLStore(r.deps.Storage.Database)
	r.deps.Workflows.Manager.Fanout.Progress = r.Progress
	r.deps.Sessions.Manager.SetProgressStore(r.Progress)
	if err := native.RegisterUpdateProgressTool(r.deps.Execution.Host.Registry, r.Progress, r.rootSessionKey, r.deps.Sessions.Manager.Coordinator.ProgressClosure); err != nil {
		return fmt.Errorf("update_progress tool: %w", err)
	}
	if err := native.RegisterCompleteLegTool(r.deps.Execution.Host.Registry, r.deps.DecodeCompleteLeg); err != nil {
		return fmt.Errorf("complete_leg tool: %w", err)
	}
	if err := native.RegisterRecallTool(r.deps.Execution.Host.Registry, recall.NewService(r.deps.Storage.Database, r.deps.Storage.Directory)); err != nil {
		return fmt.Errorf("recall tool: %w", err)
	}
	return nil
}

func (r *Runtime) wireVisualAndRenderTools(ctx context.Context) error {
	artifactRecords := visual.NewRecords(r.deps.Storage.Database, r.deps.Events.Outbox, visual.ArtifactProjection{
		Write: func(ctx context.Context, tx *sql.Tx, projectID string, rec visual.ArtifactRecord) error {
			return search.ProjectArtifactTx(ctx, tx, projectID, search.ProjectArtifactInput{
				ID:             rec.ID,
				Hash:           rec.ContentHash,
				Mime:           rec.Mime,
				Source:         rec.Source,
				Caption:        rec.Caption,
				EvidenceHandle: rec.EvidenceHandle,
				SessionID:      rec.SessionID,
				WorkflowRunID:  rec.WorkflowRunID,
				ToolCallID:     rec.ToolCallID,
				CreatedAt:      rec.CreatedAt,
			})
		},
		Delete: search.DeleteArtifactProjectionTx,
	})
	r.deps.Storage.Sessions.SetArtifactRecords(artifactRecords)
	r.Visual = visual.NewDurableStore(visual.DurableConfig{
		DataDir: r.deps.Storage.Directory,
		ArtifactsDir: func(projectID string) (string, error) {
			return project.HostSubdir(r.deps.Storage.Directory, projectID, "artifacts")
		},
		Lookup: func(ctx context.Context, sessionID string) (string, error) {
			sess, err := r.deps.Storage.Sessions.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return "", err
			}
			return sess.ProjectID, nil
		},
		ActiveRun: func(ctx context.Context, sessionID string) (string, error) {
			if r.deps.Workflows == nil || r.deps.Workflows.Manager == nil {
				return "", nil
			}
			run, err := r.deps.Workflows.Manager.Store.Runs.ActiveBySession(ctx, sessionID)
			if err != nil || run == nil {
				return "", err
			}
			return run.ID, nil
		},
		Records: artifactRecords,
	})
	r.deps.Sessions.Manager.SetVisualStore(r.Visual)
	releaseVisual := providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		root := sessiontree.RootID(context.WithoutCancel(ctx), r.deps.Storage.Sessions, sessionID)
		res := r.Visual.Resolve(context.WithoutCancel(ctx), root, artifactID)
		if !res.IsPresent() {
			return nil, "", false
		}
		return res.Bytes(), res.Meta().Mime, true
	})
	r.deps.Resources.Track("visual-bytes-resolver", 22, func(context.Context) error { releaseVisual(); return nil })
	if r.deps.Workflows != nil && r.deps.Workflows.Manager != nil {
		r.deps.Workflows.Manager.SetVisualArtifacts(r.Visual, r.rootSessionKey)
	}
	if err := visual.RegisterTestProducer(r.deps.Execution.Host.Registry); err != nil {
		return fmt.Errorf("emit_visual_fixture tool: %w", err)
	}
	browserCache := browserengine.ManagedCacheDir()
	renderBudgets, err := browser.LoadRenderBudgets()
	if err != nil {
		return fmt.Errorf("render budgets: %w", err)
	}
	r.BrowserRaster = browser.NewRasterizer(browserCache, renderBudgets)
	handleStore := renderhandle.NewStore()
	if err := native.RegisterRenderViewTool(r.deps.Execution.Host.Registry, r.deps.Execution.Host.Boundary, r.BrowserRaster, handleStore); err != nil {
		return fmt.Errorf("render_view tool: %w", err)
	}
	if err := r.deps.Sessions.Manager.Resources.RegisterCleanup("render-handles", 54, func(_ context.Context, sessionID string) error {
		handleStore.Release(sessionID)
		return nil
	}); err != nil {
		return err
	}
	matcher, _ := r.deps.Security.LoadMatcher(ctx, r.deps.TestSecretMatcher)
	screen := visualscreen.NewGate(visualscreen.NewScanner(nil).WithRenderedReferences(browser.RenderLoadsReference), matcher, r.deps.Security.Ask(r.deps.Execution.Host.Executor.Secrets, r.deps.Execution.Host.Authority.ApprovalsDisabled))
	if err := native.RegisterViewImageTool(r.deps.Execution.Host.Registry, page.ViewImageDeps{
		Boundary:      r.deps.Execution.Host.Boundary,
		Raster:        r.BrowserRaster,
		HandleStore:   handleStore,
		VisualStore:   r.Visual,
		Screen:        screen,
		RootSessionID: r.rootSessionKey,
	}); err != nil {
		return fmt.Errorf("view_image tool: %w", err)
	}
	r.BrowserPool = browser.NewPool(browserCache)
	pool := r.BrowserPool
	r.deps.Resources.Track("browser-pool", 60, func(context.Context) error { pool.Close(); return nil })
	if err := native.RegisterViewVideoTool(r.deps.Execution.Host.Registry, page.ViewVideoDeps{
		Boundary: r.deps.Execution.Host.Boundary,
		Pool:     r.BrowserPool,
		Screen:   screen,
		MaxBytes: promptattach.Active().Video.MaxBody.Int64(),
	}); err != nil {
		return fmt.Errorf("view_video tool: %w", err)
	}
	return nil
}

func (r *Runtime) wireDecisionAndCallTools() error {
	r.deps.Sessions.Decisions = decisions.NewSQL(r.deps.Storage.Database)
	r.deps.Sessions.Manager.SetDecisionStore(r.deps.Sessions.Decisions)
	r.AnswerDecision = &worker.AnswerDecisionService{
		Queue:     r.deps.Delegations.Queue,
		Decisions: r.deps.Sessions.Decisions,
		Resolver:  worker.NewSQLDecisionResolver(r.deps.Storage.Sessions, r.deps.Delegations.Queue),
		Reject:    r.deps.Execution.Rejections,
	}
	if err := native.RegisterRequestDecisionTool(r.deps.Execution.Host.Registry, workertools.RequestDecisionDeps{
		Recorder:      r.deps.Sessions.Decisions,
		Artifacts:     r.Visual,
		RootSessionID: r.rootSessionKey,
	}); err != nil {
		return fmt.Errorf("request_decision tool: %w", err)
	}
	callLookup := call.StoreSessionLookup{Get: func(ctx context.Context, id string) (string, error) {
		sess, err := r.deps.Storage.Sessions.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if sess == nil {
			return "", call.ErrSessionNotFound
		}
		return sess.WorkspacePath, nil
	}}
	r.Calls = call.NewSQLManager(r.deps.Storage.Database, callLookup)
	r.deps.Sessions.Manager.Workers.Workspaces.SetCalls(r.Calls)

	r.Snapshot.Touches = r.deps.Sessions.Manager.Workers.Workspaces.Touches
	r.Snapshot.ActiveReservations = r.deps.Sessions.Manager.Workers.Workspaces.ReservationEntries

	r.deps.Sessions.Manager.SetWorkerQueue(r.deps.Delegations.Queue)
	r.deps.Sessions.Manager.SetWorkerRounds(r.deps.WorkersCfg.RoundPolicy())
	r.deps.Sessions.Manager.SetSessionWorkerAbort(r.deps.Delegations.Cancel)
	roster := workerroster.Source{
		Reader:  workerops.Reader{Records: r.WorkerRecords, Jobs: r.deps.Delegations.Queue, Decisions: r.deps.Sessions.Decisions},
		Touches: r.deps.Sessions.Manager.Workers.Workspaces.Touches,
	}
	r.deps.Sessions.Manager.SetWorkerRoster(roster.Template)
	r.deps.Sessions.Manager.SetWorkerResultNotices(roster.ResultNotice)
	r.ParentWaiter = worker.NewParentWorkerWaiter()

	if err := call.RegisterHandoffTools(r.deps.Execution.Host.Registry, call.HandoffToolDeps{Calls: r.Calls, Sessions: callLookup}); err != nil {
		return fmt.Errorf("handoff tools: %w", err)
	}
	return nil
}

func (r *Runtime) wireApprovalRationaleAttacher() {
	if r.deps.Execution.Host == nil || r.deps.Sessions.Checkpoints == nil || r.deps.Sessions.Manager == nil || r.Progress == nil {
		return
	}
	var enabledFn func() bool
	if r.deps.Settings.Service != nil && r.deps.Settings.Service.Approvals != nil {
		perms := r.deps.Settings.Service.Approvals
		enabledFn = perms.AIRationaleEnabled
	}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Cause:       briefingstore.NewCausal(r.deps.Storage.Database),
		Workers:     r.deps.Delegations.Queue,
		Progress:    r.Progress,
		Root:        sessionRootResolver{store: r.deps.Storage.Sessions},
		Generator:   briefing.NewModelGenerator(r.deps.Providers.Service, r.deps.Providers.Costs),
		Checkpoints: r.deps.Sessions.Checkpoints,
		EnabledFn:   enabledFn,
	})
	r.deps.Execution.Host.Executor.Approvals.SetAIRationaleAttacher(attacher)
}

type sessionRootResolver struct {
	store session.Store
}

func (s sessionRootResolver) RootSessionID(ctx context.Context, sessionID string) string {
	return sessiontree.RootID(ctx, s.store, sessionID)
}

// ReleaseAccounted lets the checklist latch release worker finishes whose
// results an operation accounted for.
func (r *Runtime) ReleaseAccounted(sessionID string, jobIDs []string) {
	ctx := context.Background()
	r.deps.Sessions.Manager.Coordinator.ProgressClosure.Release(r.rootSessionKey(ctx, sessionID), jobIDs)
}
