package app

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"path/filepath"
	"strings"
)

// sessionWiring wires the session manager, its authorization and checkpoints, and secret handling.
type sessionWiring struct{ *serveBuilder }

// A settled repository brief refreshes a bounded set of session boards.
const boardRepublishSessionLimit = 64

func (b sessionWiring) wireSessionManager() error {
	if configdir.IsHarnessChannel() && b.llmSvc != nil {
		preparation, err := harnessfixture.NewPreludeController(b.dataDir, b.store)
		if err != nil {
			return err
		}
		b.llmSvc.Preparation = preparation
	}
	b.mgr = session.NewManagerWithLLMService(b.store, b.mockLLM, b.llmSvc, b.toolReg, b.sessionCfg, b.costTracker)
	b.mgr.SetMintedCredentialSource(b.detections.mintedCredentialSource)
	invocations := invocation.NewSQLRecorder(b.db)
	b.invocations = invocations
	b.mgr.SetInvocationRecorder(invocations)
	if err := b.registerSessionCrashRecovery(invocations); err != nil {
		return err
	}
	if err := b.configureSessionManager(); err != nil {
		return err
	}
	b.wireSessionToolSources()
	b.wireWorkerToolBudget()
	if err := b.wireSessionAuthorization(); err != nil {
		return err
	}
	if compactor, err := loadCompactor(b.llmSvc, b.configRoot, b.costTracker); err == nil && compactor != nil {
		b.mgr.SetCompactor(compactor)
	}
	return nil
}

func (b sessionWiring) configureSessionManager() error {
	b.mgr.SetSourceLedger(b.sourceLedger)
	b.mgr.SetAgentRegistry(b.agentRegistry)
	b.mgr.SetHostResources(b.hostResources)
	if b.hostResources != nil && b.settingsSvc != nil && b.settingsSvc.Approvals != nil {
		b.hostResources.SetPolicyBinder(newHostResourcePolicyBinder(b.settingsSvc.Approvals, b.mgr))
	}
	promptLayers := prompts.PromptLayers{
		ModuleRoot: b.configRoot,
		Site:       prompts.SitePromptFilesDir(b.configRoot),
	}
	b.promptEngine = prompts.NewFileTemplateEngineLayers(promptLayers)
	b.mgr.SetPromptEngine(b.promptEngine)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(b.promptEngine))
	b.mgr.SetPostureRegistry(b.postureRegistry)
	b.mgr.SetProjectRegistry(b.registry)
	b.mgr.SetDataDir(b.dataDir)
	b.mgr.SetScratchFolders(scratch.New(b.dataDir))
	if b.store != nil {
		b.store.SetDataDir(b.dataDir)
	}
	b.mgr.SetDoomLoopGuard(loopguard.NewMemoryDoomLoopGuard())
	b.mgr.SetRejectFormatter(b.rejectFmt)
	b.mgr.SetProfileRuntimeRules(loadProfileRuntimeRules())
	if err := progress.InitProgressGatedTools(b.configRoot); err != nil {
		return fmt.Errorf("init progress-gated tools: %w", err)
	}
	b.mgr.SetToolInvoker(b.toolRuntime.Executor)
	if b.settingsSvc != nil {
		if b.cfg.TestSessionLimits == nil {
			b.mgr.SetLimitsProvider(settings.ProjectLimitsAdapter{Store: b.settingsSvc.Limits})
		}
		b.mgr.SetEffectiveCatalogDeps(b.configRoot, b.effective, b.settingsSvc.TrustSurfaces)
		b.mgr.SetSkillsGate(b.projectSkillsGate())
	}
	if b.viewCache != nil {
		b.mgr.Catalog().SetCatalogViewCache(b.viewCache)
	}
	return nil
}

func (b sessionWiring) wireSessionToolSources() {
	if b.toolRuntime != nil && b.toolRuntime.Boundary != nil {
		mgr := b.mgr
		b.toolRuntime.Boundary.SetProfileSource(func(ctx context.Context, sessionID string) []sandbox.ToolProfile {
			view := mgr.Catalog().ViewForSessionID(ctx, sessionID)
			if view == nil {
				return nil
			}
			return view.ToolProfiles
		})
		if b.toolRuntime.Executor != nil {
			b.toolRuntime.Executor.SetToolSchemaSource(func(ctx context.Context, sessionID string) *toolschema.Config {
				view := mgr.Catalog().ViewForSessionID(ctx, sessionID)
				if view == nil {
					return nil
				}
				return view.ToolSchemas
			})
		}
	}
	if b.toolRuntime != nil {
		mgr := b.mgr
		b.toolRuntime.SetApprovalRuleSource(mgr)
		b.toolRuntime.Executor.SetHostResourceConnectionSource(b.hostResources.ResolveAction)
		b.toolRuntime.SetSkillsCatalog(func(ctx context.Context, tctx tools.ToolContext) []skills.Skill {
			roots := make([]string, 0, len(tctx.Roots))
			for _, r := range tctx.Roots {
				if path := strings.TrimSpace(r.Path); path != "" {
					roots = append(roots, path)
				}
			}
			sess, _ := mgr.SessionByID(ctx, tctx.SessionID)
			loaded, _ := mgr.EffectiveSkillsForProfile(ctx, sess, tctx.Agent, roots)
			return loaded
		})
		b.toolRuntime.SetSkillTemplateVars(func(_ context.Context, tctx tools.ToolContext) map[string]any {
			budget := spawn.DefaultWorkerToolBudget()
			if b.workerToolBudgetFor != nil {
				budget = b.workerToolBudgetFor(strings.TrimSpace(tctx.ActiveRootPath()))
			}
			return spawn.PolicyTemplateVars(budget)
		})
		b.toolRuntime.SetSkillPackConfiguration(
			func(ctx context.Context, tctx tools.ToolContext, packID string) map[string]any {
				view := mgr.Catalog().ViewForSessionID(ctx, tctx.SessionID)
				if view == nil {
					return nil
				}
				return view.Contributions.SettingsForPack(packID)
			})
	}
}

func (b sessionWiring) wireWorkerToolBudget() {
	b.workerToolBudgetFor = func(projectDir string) spawn.WorkerToolBudget {
		if b.settingsSvc == nil {
			return b.sessionCfg.WorkerToolBudget()
		}
		// Project limits require project settings trust.
		if !b.projectSettingsGate().AppliesPath(context.Background(), projectDir) {
			projectDir = ""
		}
		return settings.ProjectLimitsAdapter{Store: b.settingsSvc.Limits}.SessionLimits(projectDir).WorkerToolBudget()
	}
}

func (b sessionWiring) wireSessionAuthorization() error {
	if b.db != nil {
		auditCfg, err := authzcontext.LoadAuditConfig(b.configRoot)
		if err != nil {
			return fmt.Errorf("audit config: %w", err)
		}
		b.authzCapturer = authzcontext.NewSQLCapturer(b.db, auditCfg, authzcontext.ProfileMap(b.toolProfiles))
		cap := b.authzCapturer
		if cap != nil && cap.Sealer != nil {
			if b.settingsSvc != nil {
				cap.Sealer.Perms = b.settingsSvc.Approvals
			}
			if b.toolRuntime != nil {
				cap.Sealer.ChatGrants = func(chatSessionID string) []hitl.ApprovalGrant {
					gate := b.toolRuntime.ApprovalGate()
					if gate == nil {
						return nil
					}
					return gate.ListGrants(chatSessionID)
				}
			}
			if b.mcpReg != nil {
				cap.Sealer.MCPInventory = func(context.Context) authzcontext.MCPInventory {
					return authzcontext.MCPInventory{ProviderIDs: b.mcpReg.EnabledProviderIDs()}
				}
			}
			cap.Sealer.ToolAccess = b.mgr.ResolveToolAccess
			if b.toolRuntime != nil && b.toolRuntime.Registry != nil {
				cap.Sealer.RegisteredTools = func() []string {
					metas := b.toolRuntime.Registry.List()
					out := make([]string, 0, len(metas))
					for _, meta := range metas {
						out = append(out, meta.Name)
					}
					return out
				}
			}
			// Workflows narrow the session's spawn set.
			cap.Sealer.SpawnAllowlist = func(ctx context.Context, sess *api.Session) []string {
				if b.workflowMgr != nil && sess != nil {
					if roster := b.workflowMgr.Policy.AllowedAgents(ctx, sess.ID); len(roster) > 0 {
						return roster
					}
				}
				return spawn.AmbientAllowedAgents()
			}
			cap.Sealer.WorkerToolBudget = b.workerToolBudgetFor
			b.mgr.SetAuthzSealer(cap.Sealer)
		}
	}
	return nil
}

// registerSessionCrashRecovery orders invocation and transcript repair.
func (b sessionWiring) registerSessionCrashRecovery(invocations *invocation.SQLRecorder) error {
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "tool-invocations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: func(ctx context.Context) error {
			_, err := invocations.InterruptRunning(ctx)
			return err
		},
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "session-turns", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseBuild,
		After: []string{"tool-invocations"},
		Run:   b.mgr.RecoverOrphanedTurns,
	}); err != nil {
		return err
	}
	return delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "transcript-invocations", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"tool-invocations", "session-turns"},
		Run:   b.mgr.RecoverInterruptedToolResults,
	})
}

func (b sessionWiring) wireCheckpointRuntime() error {
	if b.authzCapturer == nil {
		return fmt.Errorf("authz: capturer required before checkpoint manager wiring")
	}
	checkpointStore := hitl.NewSQLStore(b.db)
	checkpointStore.SetEventOutbox(b.eventOutbox)
	checkpointMgr := hitl.NewManager(checkpointStore, b.eventPub, b.authzCapturer.Recorder)
	checkpointMgr.SetSessionAdmission(b.mgr.WithSessionTreeAdmission)
	checkpointMgr.SetVaultUnlock(b.presenceBroker, b.vaultUnlocks, unlockRecorder{})
	b.toolRuntime.Executor.SetPresenceAvailable(checkpointMgr.PresenceAvailable)
	checkpointMgr.SetCheckpointWaitObserver(b.mgr.BeginCheckpointWait)
	var authzRec authzledger.Recorder = b.authzCapturer.Recorder
	if b.toolRuntime != nil {
		b.toolRuntime.SetAuthzRecorder(authzRec)
	}
	b.checkpointMgr = checkpointMgr
	b.resources.track("checkpoint-expiry", 65, func(context.Context) error { checkpointMgr.StopExpiryTimers(); return nil })
	b.mgr.SetSessionCheckpointStop(checkpointMgr)
	b.mgr.SetExecutionCheckpoints(checkpointMgr)
	b.toolRuntime.SetCheckpointManager(b.checkpointMgr)
	if err := b.wireGrantedAccess(); err != nil {
		return err
	}
	if b.secretHarvest != nil {
		b.mgr.SetCredentialFiles(b.newCredentialFiles())
	}
	b.wireCredentialObservations()
	b.toolRuntime.Executor.SetSecretExposureSource(func(ctx context.Context, chatSessionID string) (bool, error) {
		return b.store.SessionSecretExposure(ctx, chatSessionID)
	})
	b.toolRuntime.Executor.SetUntrustedIngestionSource(func(ctx context.Context, chatSessionID string) (bool, error) {
		return b.store.SessionUntrustedContentResult(ctx, chatSessionID)
	})
	// Observe posture delegates mediated destinations to the grant gate.
	confine.SetUntrustedIngestionSource(untrustedIngestionStore{store: b.store})
	b.toolRuntime.Executor.SetSessionHostLedger(b.store)
	writeRootRT := approvalstate.NewSandboxPathGrantRuntime()
	b.sandboxWriteRootRT = writeRootRT
	b.mgr.SetSandboxPathGrantRuntime(writeRootRT)
	readPathRT := approvalstate.NewSandboxPathGrantRuntime()
	b.sandboxReadPathRT = readPathRT
	listenRT := approvalstate.NewSandboxPortGrantRuntime()
	b.sandboxListenRT = listenRT
	b.mgr.SetSandboxListenRuntime(listenRT)
	loopbackRT := approvalstate.NewSandboxPortGrantRuntime()
	b.sandboxLoopbackRT = loopbackRT
	b.mgr.SetSandboxLoopbackRuntime(loopbackRT)
	loopbackProv := session.NewLoopbackProvenance()
	b.mgr.SetLoopbackProvenance(loopbackProv)

	sensitiveDests, err := approvals.LoadMergedConsequenceBandPaths(b.dataDir)
	if err != nil {
		return fmt.Errorf("consequence-band paths: %w", err)
	}
	deriver := checkpointConsequenceDeriver{dests: sensitiveDests}
	b.toolRuntime.Executor.SetConsequenceDeriver(deriver)
	locations, locErr := sensitivepath.Load(
		sensitivepath.Bundled(),
		sensitivepath.Dir(filepath.Join(b.dataDir, "ask-triggers")),
	)
	if locErr != nil {
		slog.Warn("sensitive locations catalog unavailable", "error", locErr)
	}
	b.toolRuntime.SetSandboxWriteRootGate(&session.WriteRootCheckpointBroker{
		Checkpoints:       b.checkpointMgr,
		Store:             b.store,
		Runtime:           writeRootRT,
		ReadRuntime:       readPathRT,
		Consequence:       deriver,
		Authority:         b.toolRuntime.ApprovalGate(),
		ApprovalsDisabled: b.toolRuntime.ApprovalsDisabled,
		Posture:           b.toolRuntime.ApprovalPosture,
		Rule:              b.toolRuntime.WriteRootRule,
		Locations:         locations,
		Authz:             authzRec,
	})
	b.toolRuntime.SetSandboxListenGate(&session.ListenCheckpointBroker{
		Checkpoints:       b.checkpointMgr,
		Store:             b.store,
		Runtime:           listenRT,
		Loopback:          loopbackRT,
		Authority:         b.toolRuntime.ApprovalGate(),
		ApprovalsDisabled: b.toolRuntime.ApprovalsDisabled,
		Posture:           b.toolRuntime.ApprovalPosture,
		Provenance:        loopbackProv,
		Authz:             authzRec,
	})
	b.toolRuntime.SetSandboxLoopbackGate(&session.LoopbackCheckpointBroker{
		Checkpoints:       b.checkpointMgr,
		Store:             b.store,
		Runtime:           loopbackRT,
		Provenance:        loopbackProv,
		Authority:         b.toolRuntime.ApprovalGate(),
		ApprovalsDisabled: b.toolRuntime.ApprovalsDisabled,
		Posture:           b.toolRuntime.ApprovalPosture,
		Authz:             authzRec,
	})
	b.toolRuntime.SetLocalNetworkGate(&session.LocalNetworkCheckpointBroker{
		Checkpoints:       b.checkpointMgr,
		Store:             b.store,
		Listen:            listenRT,
		Loopback:          loopbackRT,
		Provenance:        loopbackProv,
		Authority:         b.toolRuntime.ApprovalGate(),
		ApprovalsDisabled: b.toolRuntime.ApprovalsDisabled,
		Posture:           b.toolRuntime.ApprovalPosture,
		Authz:             authzRec,
	})
	toolApprovalRT := b.wireAskSpamGuards()
	if err := b.wireExceptionalCapability(); err != nil {
		return err
	}
	return b.wireToolApprovalCheckpointHooks(toolApprovalRT)
}

func (b sessionWiring) assertAuthzCapturer() error {
	if b.db == nil {
		return nil
	}
	if b.authzCapturer == nil {
		return fmt.Errorf("authz: capturer must be wired when database is configured")
	}
	if b.authzCapturer.Store == nil || b.authzCapturer.Sealer == nil || b.authzCapturer.Ledger == nil {
		return fmt.Errorf("authz: capturer incomplete")
	}
	if b.mgr == nil || !b.mgr.AuthzSealWired() {
		return fmt.Errorf("authz: session manager missing authz sealer")
	}
	return nil
}

// wireAskSpamGuards installs per-chat approval counters.
func (b sessionWiring) wireAskSpamGuards() *approvalstate.ToolApprovalCoalesce {
	toolApprovalRT := approvalstate.NewToolApprovalCoalesce()
	b.mgr.SetToolApprovalCoalesce(toolApprovalRT)
	b.toolRuntime.SetToolApprovalCoalesce(toolApprovalRT)
	gateRepeatRT := approvalstate.NewGateRepeatLedger()
	b.mgr.SetGateRepeatLedger(gateRepeatRT)
	b.toolRuntime.SetGateRepeatLedger(gateRepeatRT)
	// The API server is built later; wireServer hands it the same ledger.
	b.gateRepeatRT = gateRepeatRT
	return toolApprovalRT
}

// wireToolApprovalCheckpointHooks restores pending approval joiners.
func (b sessionWiring) wireToolApprovalCheckpointHooks(toolApprovalRT *approvalstate.ToolApprovalCoalesce) error {
	mgr, ok := b.checkpointMgr.(*hitl.Manager)
	if !ok {
		return nil
	}
	mgr.SetToolApprovalTerminalHook(func(chatSessionID, grantKey string, status hitl.DecisionStatus) {
		toolApprovalRT.ClearPending(chatSessionID, grantKey)
		switch status {
		case hitl.DecisionStatusRejected:
			toolApprovalRT.RecordDeny(chatSessionID, grantKey)
		case hitl.DecisionStatusApproved:
			toolApprovalRT.ClearDeny(chatSessionID, grantKey)
		case hitl.DecisionStatusPending, hitl.DecisionStatusExpired, hitl.DecisionStatusCanceled:
		}
	})
	mgr.SetToolApprovalRestoreHook(func(row hitl.StoredCheckpoint) {
		chat, _ := row.Payload["coalesce_chat"].(string)
		if strings.TrimSpace(chat) == "" {
			chat = row.SessionID
		}
		key, _ := row.Payload["coalesce_grant_key"].(string)
		joined := 1
		switch value := row.Payload["joined_count"].(type) {
		case int:
			joined = value
		case float64:
			joined = int(value)
		}
		var ids []string
		if values, ok := row.Payload["joined_tool_call_ids"].([]any); ok {
			for _, value := range values {
				if id, ok := value.(string); ok {
					ids = append(ids, id)
				}
			}
		}
		toolApprovalRT.RestorePending(chat, key, row.ID, joined, ids)
	})
	mgr.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) {
		chat, _ := row.Payload["coalesce_chat"].(string)
		if strings.TrimSpace(chat) == "" {
			chat = row.SessionID
		}
		key, _ := row.Payload["coalesce_grant_key"].(string)
		if strings.TrimSpace(key) != "" {
			toolApprovalRT.RecordDeny(chat, key)
		}
	})
	if err := mgr.RestorePending(context.Background()); err != nil {
		return fmt.Errorf("restore pending checkpoints: %w", err)
	}
	if err := mgr.RestoreRejectedToolApprovalDenials(context.Background()); err != nil {
		return fmt.Errorf("restore rejected tool-approval denials: %w", err)
	}
	return nil
}

// wireGrantedAccess installs chat and durable filesystem grants.
func (b sessionWiring) wireGrantedAccess() error {
	grantedRT := grantedpath.NewRuntime()
	b.grantedPathRT = grantedRT
	// Durable grants are read through their revocation source.
	approvalGate := b.toolRuntime.ApprovalGate()
	grantedRT.SetDurableSource(func(projectID string) []grantedpath.Grant {
		if approvalGate == nil {
			return nil
		}
		return durableGrantedPaths(approvalGate.ListGrants(""), projectID)
	})
	projectpaths.SetGrantedAccessSource(func(rootSession, projectID, abs string, write bool) (projectpaths.Access, bool) {
		mode := grantedpath.ModeRead
		if write {
			mode = grantedpath.ModeWrite
		}
		g, ok := grantedRT.Covers(rootSession, projectID, abs, mode)
		if !ok {
			return projectpaths.Access{}, false
		}
		return projectpaths.Access{Path: g.Path, Tree: g.Tree}, true
	})
	if err := b.mgr.RegisterSessionCleanup("approval-run", 50, func(_ context.Context, sessionID string) error {
		b.toolRuntime.ReleaseSessionRun(sessionID)
		b.sandboxReadPathRT.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	// Chat approvals outlive Stop and end when the chat is disposed.
	if err := b.mgr.RegisterSessionDisposal("approvals", 50, func(_ context.Context, sessionID string) error {
		b.toolRuntime.ForgetSessionAuthorization(sessionID)
		grantedRT.Forget(sessionID)
		b.sandboxReadPathRT.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}
	// The secret matcher loads lazily.
	return b.mgr.RegisterSessionCleanup("harvested-secrets", 53, func(_ context.Context, sessionID string) error {
		b.secretHarvest.Forget(sessionID)
		return nil
	})
}

// untrustedIngestionStore leaves failed reads unestablished.
type untrustedIngestionStore struct {
	store session.Store
}

func (u untrustedIngestionStore) SessionIngestedUntrusted(ctx context.Context, chatSessionID string) bool {
	if u.store == nil {
		return false
	}
	ingested, err := u.store.SessionUntrustedContentResult(ctx, chatSessionID)
	return err == nil && ingested
}
