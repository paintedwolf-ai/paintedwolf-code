package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// boardWiring wires the board, research, grounding, and workflow subsystems.
type boardWiring struct{ *serveBuilder }

func (b boardWiring) wireBoardAndResearch() error {
	var err error
	b.repoProvider = repoinfo.NewProvider(sourcecatalog.Process().Trees, b.repoCatalogRoot, filepath.Join(enginepaths.RepoOrientationRootUnder(b.dataDir), "v1"))
	b.resources.track("source-catalog", 86, sourcecatalog.Process().Drain)
	b.mgr.SetRepoProvider(b.repoProvider)
	// Background brief completion publishes its own board update.
	b.repoProvider.SetOnSettled(func(projectDir string) {
		if b.eventPub != nil {
			b.eventPub.PublishBoardForRoot(context.Background(), projectDir)
		}
	})
	scopeCfg, scopeErr := toolscope.Load()
	if scopeErr != nil {
		return fmt.Errorf("toolscope: %w", scopeErr)
	}
	b.toolRuntime.Survey.SetScopeGuards(scopeCfg, b.repoCatalogFileCount)
	b.boardSnap = &board.SnapshotBuilder{
		Delegations:            b.delegationStore,
		Workers:                b.workerQueue,
		Workflow:               b.workflowMgr,
		Repo:                   b.repoProvider,
		Git:                    b.gitMgr,
		StatusCache:            b.gitStatusCache,
		RepoSets:               git.NewRepoSetCache(git.DefaultStatusCacheTTL),
		Projects:               b.registry,
		Scans:                  b.scanCoordinator,
		ScanCompare:            b.scanCoordinator,
		SecurityScanners:       b.settingsSvc.SecurityScanners,
		OverlayGate:            b.projectSettingsGate(),
		DefaultExecutionTarget: worker.DefaultExecutionTarget(b.workersCfg),
		Cost:                   b.costTracker,
		CostTrackingEnabled: func() bool {
			return b.settingsSvc != nil && b.settingsSvc.Pricing != nil && b.settingsSvc.Pricing.Effective().CostTrackingEnabled
		},
		Worktree: b.mgr.BoardGitWorktreeFunc(b.gitMgr),
	}
	b.mgr.SetBoardInject(&board.InjectBuilder{SnapshotBuilder: b.boardSnap, Projects: b.registry}, board.DefaultInjectFormatter())
	b.mgr.SetIncludeScanLegend(func() bool {
		if b.settingsSvc == nil || b.settingsSvc.SecurityScanners == nil {
			return true
		}
		return b.settingsSvc.SecurityScanners.Effective().Enabled
	})
	if err := board.RegisterBoardTools(b.toolRuntime.Registry, board.ToolDeps{
		Builder:            b.boardSnap,
		Findings:           func() findings.Store { return b.findingsStore },
		RootSession:        b.rootSessionKey,
		PromotePaths:       b.mgr.PromotePathBoardLines,
		OverlayMergePlan:   b.mgr.OverlayMergePlanFn(),
		ActiveReservations: b.mgr.ActiveReservationBoardEntries,
	}); err != nil {
		return fmt.Errorf("board tools: %w", err)
	}
	b.toolRuntime.Survey.SetListDirUnionBrief(func(ctx context.Context, tctx tools.ToolContext, subpath string) (string, error) {
		if len(tctx.Source.Roots) < 2 {
			return "", nil
		}
		if subpath != "" && !projectroot.IsUnionDiscoveryPath(subpath) {
			return "", nil
		}
		mrb, err := repoinfo.AnalyzeRoots(ctx, tctx.Source.Roots, repoinfo.DefaultBriefBudget(), b.repoProvider)
		if err != nil {
			return "", err
		}
		return repoinfo.FormatOrientationBriefText(mrb.OrientationRoots()), nil
	})
	b.mgr.SetTurnLoads(b.turnLoads)
	b.mgr.SetDecider(b.decider)
	b.mgr.SetSkillBodyRenderer(b.toolRuntime.Skills.RenderSkillBody)
	b.webResearchRuntime, err = webresearch.WireRuntime()
	if err != nil {
		return fmt.Errorf("web research runtime: %w", err)
	}
	b.webResearchCreds = b.webResearchRuntime.Creds
	webCat, webCfg, webReg := b.webResearchRuntime.Catalog, b.webResearchRuntime.Config, b.webResearchRuntime.Registry
	var llmReg, llmPol = b.llmRegistryPolicy()
	b.webDiscoverer = webresearch.NewDirectDiscovererFactory(b.webIndex, webReg, b.webResearchCreds, webCfg, webCat, b.rerank)
	b.toolRuntime.Web.SetDirectDiscovererFactory(b.webDiscoverer)
	if b.webIndex != nil {
		webReg.AttachQuotaStore(b.webIndex)
		// Session activity and schedules warm the index.
		b.webWarmer = webresearch.NewWarmer(b.webIndex, llmReg, llmPol, webCfg)
		b.webWarmer.Cost = b.costTracker
		if b.llmSvc != nil {
			b.webWarmer.Plane = b.llmSvc.Utility
		}
		b.mgr.SetIndexWarmer(b.webWarmer)
		// Presence is evaluated for each warm cycle.
		b.warmRunner = &webresearch.WarmRunner{
			W: b.webWarmer, Roots: b.projectRootPaths, ProjectIDForRoot: b.projectIDForRoot, Repo: b.repoProvider,
			Live: func() bool { return b.presence.Live() },
		}
	}
	deps := webresearch.ToolDeps{
		Creds:    b.webResearchCreds,
		Config:   webCfg,
		Catalog:  webCat,
		Registry: webReg,
		Index:    b.webIndex,
		Rerank:   b.rerank,
		Boundary: b.toolRuntime.Boundary,
		SearchWarmHook: func(ctx context.Context, sessionID, toolCallID, query, projectDir string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool) {
			b.mgr.WarmIndexForSearch(ctx, sessionID, toolCallID, query, projectDir, hitURLs, residualURLs, strongHits, maxResults, directParticipated)
		},
		FetchWarmHook: func(ctx context.Context, sessionID, toolCallID, pageURL, title, projectDir string) {
			b.mgr.WarmIndexForFetch(ctx, sessionID, toolCallID, pageURL, title, projectDir)
		},
	}
	if matcher, err := sessionWiring(b).loadSecretMatcher(); err != nil {
		return err
	} else {
		deps.SecretMatcher = matcher
		deps.SecretAsk = sessionWiring(b).secretAskFunc()
		deps.VisualStore = b.visualStore
		deps.VisualScreen = visualscreen.NewGate(visualscreen.NewScanner(nil).WithRenderedReferences(browser.RenderLoadsReference), matcher, deps.SecretAsk)
		if err := sessionWiring(b).wireSecretCapabilities(); err != nil {
			return err
		}
	}
	if err := webresearch.RegisterToolsWithFactory(b.toolRuntime.Registry, deps, b.toolRuntime.Web.DirectFactoryGetter()); err != nil {
		return fmt.Errorf("web research tools: %w", err)
	}
	if err := httpaction.Register(b.toolRuntime.Registry, httpaction.Deps{
		Boundary: b.toolRuntime.Boundary, SecretMatcher: deps.SecretMatcher, SecretAsk: deps.SecretAsk,
		Secrets: b.secretCaps,
	}); err != nil {
		return fmt.Errorf("http request tool: %w", err)
	}
	b.toolRuntime.Web.SetWebResearchConfig(webCfg)
	b.mgr.SetWebResearchConfig(webCfg)
	return nil
}

func (b boardWiring) repoCatalogFileCount(projectDir string) (int, bool) {
	ctx := context.Background()
	identity, ok, err := b.repoCatalogRoot(ctx, projectDir)
	if err != nil || !ok {
		return 0, false
	}
	snapshot, settled := sourcecatalog.Process().CurrentSettled(ctx, identity.ProjectID, []sourcecatalog.Root{{
		ID: identity.RootID, Path: projectDir,
	}})
	if !settled || snapshot.State != sourcecatalog.StateReady {
		return 0, false
	}
	count := 0
	for _, entry := range snapshot.Entries {
		if entry.RootID == identity.RootID && !entry.IsDir && !entry.TargetIsDir && !entry.IsSymlink {
			count++
		}
	}
	return count, true
}

func (b boardWiring) repoCatalogRoot(ctx context.Context, rootPath string) (repoinfo.CatalogRoot, bool, error) {
	if b.registry == nil {
		return repoinfo.CatalogRoot{}, false, nil
	}
	projects, err := b.registry.List(ctx)
	if err != nil {
		return repoinfo.CatalogRoot{}, false, err
	}
	want := filepath.Clean(strings.TrimSpace(rootPath))
	for _, proj := range projects {
		for _, root := range proj.Roots {
			if filepath.Clean(strings.TrimSpace(root.Path)) == want {
				return repoinfo.CatalogRoot{ProjectID: proj.ID, RootID: root.ID}, true, nil
			}
		}
	}
	return repoinfo.CatalogRoot{}, false, nil
}

func (b boardWiring) projectIDForRoot(ctx context.Context, rootPath string) (string, error) {
	if b.registry == nil {
		return "", nil
	}
	projects, err := b.registry.List(ctx)
	if err != nil {
		return "", err
	}
	want := filepath.Clean(strings.TrimSpace(rootPath))
	for _, proj := range projects {
		for _, root := range proj.Roots {
			if filepath.Clean(strings.TrimSpace(root.Path)) == want {
				return proj.ID, nil
			}
		}
	}
	return "", nil
}

func (b boardWiring) wireGroundingAndFindings() error {
	b.mgr.SetRuleEngine(b.ruleEngine)
	if err := b.wireGroundingCoordinators(); err != nil {
		return err
	}
	if err := b.wireFindingAndProgressTools(); err != nil {
		return err
	}
	if err := b.wireVisualAndRenderTools(); err != nil {
		return err
	}
	if err := b.wireDecisionAndCallTools(); err != nil {
		return err
	}
	b.wireApprovalRationaleAttacher()
	return nil
}

func (b boardWiring) wireGroundingCoordinators() error {
	groundingCfg, err := delegation.LoadGroundingConfig()
	if err != nil {
		return fmt.Errorf("grounding config: %w", err)
	}
	groundingGate := delegation.NewSimpleDelegationGroundingGate(groundingCfg)
	groundingGate.Criteria = b.criteriaChecker
	groundingState := grounding.NewStateStore()
	b.delegationMgr.Grounding = delegation.NewGroundingCoordinator(b.delegationStore, b.workerQueue, groundingGate, groundingCfg, groundingState, b.mgr)
	b.delegationMgr.Grounding.InspectorCloseout = b.delegationMgr.InspectorCloseout
	b.delegationMgr.Grounding.Events = b.eventPub
	b.delegationMgr.Grounding.Pipeline = b.mgr.OARPipeline()
	ambientGate := delegation.NewSimpleAmbientGroundingGate(groundingCfg)
	ambientState := grounding.NewStateStore()
	ambientCoord := delegation.NewAmbientGroundingCoordinator(b.delegationStore, b.workerQueue, ambientGate, groundingCfg, ambientState, b.mgr)
	ambientCoord.Events = b.eventPub
	ambientCoord.Pipeline = b.mgr.OARPipeline()
	b.mgr.SetGroundingHook(&delegation.ChainedGroundingCoordinator{
		Delegation: b.delegationMgr.Grounding,
		Ambient:    ambientCoord,
	})
	b.groundingSvc = &toolhost.GroundingService{
		Config:    groundingCfg,
		State:     grounding.NewStateStore(),
		Ledger:    b.store,
		RejectFmt: b.rejectFmt,
		Nudger:    b.mgr,
	}
	return nil
}

func (b boardWiring) wireFindingAndProgressTools() error {
	b.findingsStore = findings.NewSQLStore(b.db)
	b.mgr.SetFindingsStore(b.findingsStore)
	b.mgr.SetPeerRejectionFeed(session.NewPeerRejectionFeed())
	if err := native.RegisterRecordFindingTool(b.toolRuntime.Registry, reporttools.RecordFindingGates{
		Grounding: b.groundingSvc,
	}, b.findingsStore, b.rootSessionKey); err != nil {
		return fmt.Errorf("record_finding tool: %w", err)
	}
	if err := native.RegisterSurfaceNoteTool(b.toolRuntime.Registry, reporttools.SurfaceNoteDeps{
		Ledger: b.mgr.CloseoutEvidence(),
		Messages: func(ctx context.Context, sessionID string) ([]api.Message, error) {
			return b.store.GetMessages(ctx, sessionID)
		},
		Friction: func(ctx context.Context, sessionID, code string) {
			if b.mgr != nil {
				b.mgr.RecordGroundingFriction(ctx, sessionID)
			}
		},
	}); err != nil {
		return fmt.Errorf("surface_note tool: %w", err)
	}
	b.progressStore = progress.NewSQLStore(b.db)
	b.workflowMgr.Progress = b.progressStore
	b.mgr.SetProgressStore(b.progressStore)
	if err := native.RegisterUpdateProgressTool(b.toolRuntime.Registry, b.progressStore, b.rootSessionKey); err != nil {
		return fmt.Errorf("update_progress tool: %w", err)
	}
	if err := native.RegisterCompleteLegTool(b.toolRuntime.Registry, delegationWiring(b).decodeCompleteLeg); err != nil {
		return fmt.Errorf("complete_leg tool: %w", err)
	}
	// Recall reach follows session topology.
	if err := native.RegisterRecallTool(b.toolRuntime.Registry, recall.NewService(b.db, b.dataDir)); err != nil {
		return fmt.Errorf("recall tool: %w", err)
	}
	return nil
}

func (b boardWiring) wireVisualAndRenderTools() error {
	artifactRecords := visual.NewRecords(b.db, b.eventOutbox, visual.ArtifactProjection{
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
	b.store.SetArtifactRecords(artifactRecords)
	b.visualStore = visual.NewDurableStore(visual.DurableConfig{
		DataDir: b.dataDir,
		ArtifactsDir: func(projectID string) (string, error) {
			return project.HostSubdir(b.dataDir, projectID, "artifacts")
		},
		Lookup: func(ctx context.Context, sessionID string) (string, error) {
			sess, err := b.store.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return "", err
			}
			return sess.ProjectID, nil
		},
		// The producing session records its active workflow run.
		ActiveRun: func(ctx context.Context, sessionID string) (string, error) {
			if b.workflowMgr == nil {
				return "", nil
			}
			run, err := b.workflowMgr.GetActive(ctx, sessionID)
			if err != nil || run == nil {
				return "", err
			}
			return run.ID, nil
		},
		Records: artifactRecords,
	})
	b.mgr.SetVisualStore(b.visualStore)
	// Provider requests resolve image attachments from the root session's artifacts.
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		root := session.RootSessionID(b.ctx, b.store, sessionID)
		res := b.visualStore.Resolve(b.ctx, root, artifactID)
		if !res.IsPresent() {
			return nil, "", false
		}
		return res.Bytes(), res.Meta().Mime, true
	})
	if b.workflowMgr != nil {
		b.workflowMgr.SetVisualArtifacts(b.visualStore, b.rootSessionKey)
	}
	if err := visual.RegisterTestProducer(b.toolRuntime.Registry); err != nil {
		return fmt.Errorf("emit_visual_fixture tool: %w", err)
	}
	browserCache := browserengine.ManagedCacheDir()
	renderBudgets, err := browser.LoadRenderBudgets()
	if err != nil {
		return fmt.Errorf("render budgets: %w", err)
	}
	b.browserRaster = browser.NewRasterizer(browserCache, renderBudgets)
	handleStore := renderhandle.NewStore()
	if err := native.RegisterRenderViewTool(b.toolRuntime.Registry, b.toolRuntime.Boundary, b.browserRaster, handleStore); err != nil {
		return fmt.Errorf("render_view tool: %w", err)
	}
	if err := b.mgr.RegisterSessionCleanup("render-handles", 54, func(_ context.Context, sessionID string) error {
		handleStore.Release(sessionID)
		return nil
	}); err != nil {
		return err
	}
	matcher, _ := sessionWiring(b).loadSecretMatcher()
	screen := visualscreen.NewGate(visualscreen.NewScanner(nil).WithRenderedReferences(browser.RenderLoadsReference), matcher, sessionWiring(b).secretAskFunc())
	if err := native.RegisterViewImageTool(b.toolRuntime.Registry, page.ViewImageDeps{
		Boundary:      b.toolRuntime.Boundary,
		Raster:        b.browserRaster,
		HandleStore:   handleStore,
		VisualStore:   b.visualStore,
		Screen:        screen,
		RootSessionID: b.rootSessionKey,
	}); err != nil {
		return fmt.Errorf("view_image tool: %w", err)
	}
	b.browserPool = browser.NewPool(browserCache)
	if err := native.RegisterViewVideoTool(b.toolRuntime.Registry, page.ViewVideoDeps{
		Boundary: b.toolRuntime.Boundary,
		Pool:     b.browserPool,
		Screen:   screen,
		MaxBytes: promptattach.Active().Video.MaxBody.Int64(),
	}); err != nil {
		return fmt.Errorf("view_video tool: %w", err)
	}
	return nil
}

func (b boardWiring) wireDecisionAndCallTools() error {
	b.decisionStore = session.NewSQLDecisionStore(b.db)
	b.mgr.SetDecisionStore(b.decisionStore)
	b.workerBudgetLedger = worker.NewSQLBudgetLedger(b.store, b.workerQueue)
	if err := worker.RegisterRequestBudgetTool(b.toolRuntime.Registry, worker.RequestBudgetToolDeps{
		Queue:      b.workerQueue,
		Ledger:     b.workerBudgetLedger,
		ToolBudget: b.workerToolBudgetFor,
		Notify:     b.mgr.NotifyWorkerBudgetRequested,
	}); err != nil {
		return fmt.Errorf("request_budget tool: %w", err)
	}
	b.answerDecisionSvc = &worker.AnswerDecisionService{
		Queue:     b.workerQueue,
		Decisions: b.decisionStore,
		Resolver:  worker.NewSQLDecisionResolver(b.store, b.workerQueue),
		Reject:    b.rejectFmt,
	}
	if err := native.RegisterRequestDecisionTool(b.toolRuntime.Registry, workertools.RequestDecisionDeps{
		Recorder:      b.decisionStore,
		Artifacts:     b.visualStore,
		RootSessionID: b.rootSessionKey,
	}); err != nil {
		return fmt.Errorf("request_decision tool: %w", err)
	}
	callLookup := call.StoreSessionLookup{Get: func(ctx context.Context, id string) (string, error) {
		sess, err := b.store.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if sess == nil {
			return "", call.ErrSessionNotFound
		}
		return sess.WorkspacePath, nil
	}}
	b.callMgr = call.NewSQLManager(b.db, callLookup)
	b.mgr.SetCallManager(b.callMgr)
	b.mgr.SetWorkerTouchLedger(session.NewWorkerTouchLedger())
	b.boardSnap.Touches = b.mgr
	b.boardSnap.ActiveReservations = b.mgr.ActiveReservationBoardEntries

	b.mgr.SetWorkerQueue(b.workerQueue)
	b.mgr.SetSessionWorkerAbort(b.workerCancelSvc)
	b.parentWorkerWaiter = worker.NewParentWorkerWaiter()

	if err := call.RegisterHandoffTools(b.toolRuntime.Registry, call.HandoffToolDeps{Calls: b.callMgr, Sessions: callLookup}); err != nil {
		return fmt.Errorf("handoff tools: %w", err)
	}
	return nil
}

func (b boardWiring) rootSessionKey(ctx context.Context, sessionID string) string {
	return session.RootSessionID(ctx, b.store, sessionID)
}

// wireApprovalRationaleAttacher records rationale after checkpoint creation.
func (b boardWiring) wireApprovalRationaleAttacher() {
	if b.toolRuntime == nil || b.checkpointMgr == nil || b.mgr == nil || b.progressStore == nil {
		return
	}
	// An unavailable model leaves the rationale unset.
	var summarizer compaction.Summarizer = compaction.UnavailableSummarizer{}
	if b.llmSvc != nil && b.llmSvc.Registry != nil && b.llmSvc.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		summarizer = b.llmSvc.BindSummarizer(&llm.RegistrySummarizer{
			Fallback: compaction.UnavailableSummarizer{},
			Cost:     b.costTracker,
			Purpose:  "approval_rationale",
			Class:    llm.UtilityClassRequested,
		})
	}
	var enabledFn func() bool
	if b.settingsSvc != nil && b.settingsSvc.Approvals != nil {
		perms := b.settingsSvc.Approvals
		enabledFn = perms.AIRationaleEnabled
	}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages:    b.mgr,
		Workers:     b.workerQueue,
		Progress:    b.progressStore,
		Root:        sessionRootResolver{store: b.store},
		Summarizer:  summarizer,
		Checkpoints: b.checkpointMgr,
		EnabledFn:   enabledFn,
	})
	b.toolRuntime.Executor.Approvals.SetAIRationaleAttacher(attacher)
}

type sessionRootResolver struct {
	store session.Store
}

func (r sessionRootResolver) RootSessionID(ctx context.Context, sessionID string) string {
	return session.RootSessionID(ctx, r.store, sessionID)
}

// Each project's primary root precedes its additional roots.
func (b boardWiring) projectRootPaths(ctx context.Context) ([]string, error) {
	if b.registry == nil {
		return nil, nil
	}
	projects, err := b.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, proj := range projects {
		for _, root := range proj.Roots {
			if root.IsPrimary {
				out = append(out, root.Path)
			}
		}
		for _, root := range proj.Roots {
			if !root.IsPrimary {
				out = append(out, root.Path)
			}
		}
	}
	return out, nil
}
