package workflows

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/protection"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
)

// Dependencies declares required inputs for workflow initialization.
type Dependencies struct {
	Database            *db.Store
	DataDir             string
	ModuleRoot          string
	EffectiveCatalog    *extpacks.EffectiveCatalog
	Projects            project.Registry
	Sessions            *store.SQL
	SessionManager      *session.Host
	DelegationStore     *delegation.SQLStore
	EventsOutbox        *eventoutbox.Outbox
	EventPublisher      *events.Publisher
	AuthzRecorder       authzledger.TransactionalRecorder
	WebResearchConfig   *webresearch.ConfigStore
	ProjectSettingsGate *settings.ProjectSurfaceGate
}

// Build creates the workflow stores, resolver, manager, and blueprints.
func Build(ctx context.Context, deps Dependencies, registerRecovery func(bootrecovery.Entry) error) (*Runtime, error) {
	if deps.AuthzRecorder == nil {
		return nil, fmt.Errorf("authz: recorder required before workflow wiring")
	}

	blueprintStore := blueprint.NewFileStore(func(c context.Context, projectID string) (string, error) {
		p, err := deps.Projects.Get(c, projectID)
		if err != nil {
			return "", err
		}
		return project.PrimaryRootPath(p), nil
	})
	blueprintMgr := blueprint.NewManager(blueprintStore)
	blueprintMgr.SetProjects(deps.Projects)
	blueprintMgr.Approvals = blueprint.NewApprovalStore(deps.Database, deps.AuthzRecorder)
	blueprintMgr.SetDataDir(deps.DataDir)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return nil, fmt.Errorf("workflow manifests: %w", err)
	}

	sessionWorkflowStore := workflowdrafts.NewSQL(deps.Database)
	manifestResolver := workflowcatalog.Resolver{
		SessionStore: sessionWorkflowStore,
		CatalogFor: func(c context.Context, _ string, sessionID string) *extpacks.EffectiveCatalog {
			if deps.SessionManager != nil && deps.Sessions != nil && strings.TrimSpace(sessionID) != "" {
				if sess, sErr := deps.Sessions.Get(c, sessionID); sErr == nil && sess != nil {
					if cat, catErr := deps.SessionManager.Catalog.EffectiveCatalogForProject(c, sess.ProjectID); catErr == nil && cat != nil {
						return cat
					}
				}
			}
			extpacks.RefreshActiveIfStale(c)
			if live := extpacks.Active(); live != nil {
				return live
			}
			return deps.EffectiveCatalog
		},
		ProjectTierApplies: func(c context.Context, projectDir string) bool {
			if deps.ProjectSettingsGate != nil {
				return deps.ProjectSettingsGate.AppliesPath(c, projectDir)
			}
			return false
		},
	}

	workflowStore := workflowpersistence.New(deps.Database)
	workflowStore.Transactions.SetEventOutbox(deps.EventsOutbox)
	workflowStore.Transactions.SetSessionMutations(deps.Sessions)
	workflowStore.Transactions.SetAuthzRecorder(deps.AuthzRecorder)

	workflowMgr := workflow.NewManager(workflowStore, deps.Sessions, manifestRegistry, deps.EventPublisher)
	workflowMgr.Phases.ReviewSpawnFilter = func(_ context.Context, _, _ string, candidates []string) []string {
		if deps.WebResearchConfig != nil && !deps.WebResearchConfig.SearchEnabled() {
			return agentdef.FilterExternalSourceAgents(candidates)
		}
		return candidates
	}
	workflowMgr.Children.ReviewSpawnFilter = workflowMgr.Phases.ReviewSpawnFilter
	workflowMgr.Recovery.Before = time.Now().UTC()
	if deps.SessionManager != nil {
		workflowMgr.Verdicts.VerdictGrounding = deps.SessionManager.Coordinator.Closeout.EvaluateVerdictGrounding
	}
	workflowMgr.Resolver.SessionStore = manifestResolver.SessionStore
	workflowMgr.Resolver.CatalogFor = manifestResolver.CatalogFor
	workflowMgr.Resolver.ProjectTierApplies = manifestResolver.ProjectTierApplies
	workflowMgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(deps.Database)
	if deps.EventPublisher != nil {
		var protectionService *protection.Service
		if deps.SessionManager != nil {
			protectionService = deps.SessionManager.Chats.Protection
		}
		deps.EventPublisher.SessionUI = protection.UIWithProtection{Inner: workflowMgr.Presentation, Protection: protectionService}
	}
	workflowMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	workflowMgr.Blueprints.Getter = blueprintMgr
	workflowMgr.Presentation.BlueprintGetter = blueprintMgr
	workflowMgr.Approvals.Getter = blueprintMgr

	blueprintMgr.BeforeRetarget = func(c context.Context, projectID, from, to string) {
		run, bErr := workflowStore.Runs.ActiveByProjectForBlueprint(c, strings.TrimSpace(projectID), strings.TrimSpace(from))
		if bErr != nil || run == nil {
			return
		}
		if deps.SessionManager != nil {
			deps.SessionManager.Chats.Captures.RecordPrimaryMutation(c, run.SessionID, from)
			deps.SessionManager.Chats.Captures.RecordPrimaryMutation(c, run.SessionID, to)
			deps.SessionManager.Chats.Captures.RecordBlueprintBinding(c, run.SessionID, from)
		}
	}
	blueprintMgr.AfterRetarget = workflowMgr.Blueprints.RebindBlueprintPath
	blueprintMgr.ActiveRun = func(c context.Context, projectID, path string) (string, bool, error) {
		run, bErr := workflowStore.Runs.ActiveByProjectForBlueprint(c, strings.TrimSpace(projectID), strings.TrimSpace(path))
		if bErr != nil || run == nil {
			return "", false, bErr
		}
		return run.ID, true, nil
	}

	if registerRecovery != nil {
		if err := registerRecovery(bootrecovery.Entry{
			Name: "workflow-verdicts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
			Run: workflowMgr.Verdicts.RecoverVerdictOperations,
		}); err != nil {
			return nil, err
		}
		if err := registerRecovery(bootrecovery.Entry{
			Name: "workflow-review-repairs", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
			After: []string{"workflow-verdicts"}, Run: workflowMgr.Repairs.Recover,
		}); err != nil {
			return nil, err
		}
		if err := registerRecovery(bootrecovery.Entry{
			Name: "workflow-teardowns", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
			Run: workflowMgr.Controls.Cleanup.Recover,
		}); err != nil {
			return nil, err
		}
		if err := registerRecovery(bootrecovery.Entry{
			Name: "workflow-child-terminals", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
			After: []string{"workflow-teardowns"}, Run: workflowMgr.Children.RecoverTerminalChildren,
		}); err != nil {
			return nil, err
		}
	}

	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	simpleInspector := inspector.NewSimpleInspector(evidenceStore)
	simpleInspector.ProjectDir = func(c context.Context, runID string) (string, error) {
		if deps.DelegationStore == nil {
			return "", nil
		}
		r, err := deps.DelegationStore.Get(c, runID)
		if err != nil {
			return "", err
		}
		dir, err := project.EnsureHostDataDir(deps.DataDir, r.ProjectID)
		if err != nil {
			return "", err
		}
		return dir, nil
	}
	workflowMgr.Verdicts.EvidenceStore = evidenceStore
	workflowMgr.SetEvidenceProjectDir(func(c context.Context, sessionID string) (string, error) {
		if deps.Sessions == nil {
			return "", nil
		}
		sess, err := deps.Sessions.Get(c, sessionID)
		if err != nil || sess == nil {
			return "", err
		}
		if strings.TrimSpace(sess.ProjectID) == "" {
			return "", nil
		}
		return project.EnsureHostDataDir(deps.DataDir, sess.ProjectID)
	})
	workflowMgr.Verdicts.OnGateEvidencePersisted = func(c context.Context, sessionID, workflowRunID string, rec evidence.Record) {
		projectID, err := search.ResolveProjectIDForSession(c, deps.Database, sessionID)
		if err != nil || projectID == "" {
			return
		}
		_ = search.ProjectGateEvidenceComplete(c, deps.Database, search.ProjectGateEvidenceInput{
			ProjectID:     projectID,
			SessionID:     sessionID,
			WorkflowRunID: workflowRunID,
			Record:        rec,
		})
	}

	return &Runtime{
		Store:      workflowStore,
		Drafts:     sessionWorkflowStore,
		Manifests:  manifestRegistry,
		Resolver:   manifestResolver,
		Manager:    workflowMgr,
		Blueprints: blueprintMgr,
		Evidence:   evidenceStore,
		Inspector:  simpleInspector,
	}, nil
}
