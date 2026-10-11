package boards

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/session/decisions"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
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
	if err := native.RegisterRecordFindingTool(r.deps.Execution.Host.Registry, reporttools.RecordFindingGates{
		Grounding: r.Grounding,
	}, r.Findings, r.rootSessionKey); err != nil {
		return fmt.Errorf("record_finding tool: %w", err)
	}
	if err := native.RegisterSurfaceNoteTool(r.deps.Execution.Host.Registry, reporttools.SurfaceNoteDeps{
		Ledger: r.deps.Sessions.Manager.Verification.Evidence,
		Messages: func(ctx context.Context, sessionID string) ([]api.Message, error) {
			return r.deps.Storage.Sessions.GetMessages(ctx, sessionID)
		},
		Friction: func(ctx context.Context, sessionID, code string) {
			if r.deps.Sessions != nil && r.deps.Sessions.Manager != nil {
				r.deps.Sessions.Manager.Runner.Closeouts.RecordGroundingFriction(ctx, sessionID)
			}
		},
	}); err != nil {
		return fmt.Errorf("surface_note tool: %w", err)
	}
	r.Progress = progress.NewSQLStore(r.deps.Storage.Database)
	r.deps.Workflows.Manager.Fanout.Progress = r.Progress
	r.deps.Sessions.Manager.SetProgressStore(r.Progress)
	if err := native.RegisterUpdateProgressTool(r.deps.Execution.Host.Registry, r.Progress, r.rootSessionKey); err != nil {
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
	r.BudgetLedger = worker.NewSQLBudgetLedger(r.deps.Storage.Sessions, r.deps.Delegations.Queue)
	if err := worker.RegisterRequestBudgetTool(r.deps.Execution.Host.Registry, worker.RequestBudgetToolDeps{
		Queue:      r.deps.Delegations.Queue,
		Ledger:     r.BudgetLedger,
		ToolBudget: r.deps.Sessions.WorkerToolBudgetFor,
		Notify:     r.deps.Sessions.Manager.Coordinator.Workers.BudgetRequested,
	}); err != nil {
		return fmt.Errorf("request_budget tool: %w", err)
	}
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
	r.deps.Sessions.Manager.SetSessionWorkerAbort(r.deps.Delegations.Cancel)
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
	var summarizer compaction.Summarizer = compaction.UnavailableSummarizer{}
	if r.deps.Providers.Service != nil && r.deps.Providers.Service.Registry != nil && r.deps.Providers.Service.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		summarizer = r.deps.Providers.Service.BindSummarizer(&llm.RegistrySummarizer{
			Fallback: compaction.UnavailableSummarizer{},
			Cost:     r.deps.Providers.Costs,
			Purpose:  "approval_rationale",
			Class:    llm.UtilityClassRequested,
		})
	}
	var enabledFn func() bool
	if r.deps.Settings.Service != nil && r.deps.Settings.Service.Approvals != nil {
		perms := r.deps.Settings.Service.Approvals
		enabledFn = perms.AIRationaleEnabled
	}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages:    r.deps.Sessions.Manager.Runner.Transcript,
		Workers:     r.deps.Delegations.Queue,
		Progress:    r.Progress,
		Root:        sessionRootResolver{store: r.deps.Storage.Sessions},
		Summarizer:  summarizer,
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
