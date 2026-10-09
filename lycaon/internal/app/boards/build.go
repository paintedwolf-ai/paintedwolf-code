package boards

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/decisions"
	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
)

// Dependencies holds the domain runtimes and services needed by boards.
type Dependencies struct {
	Storage                  persistence.Runtime
	Sessions                 *sessions.Runtime
	Delegations              *delegations.Runtime
	Workflows                *workflows.Runtime
	Scanning                 *scanning.Runtime
	Security                 *security.Runtime
	Providers                providers.Runtime
	Execution                execution.Runtime
	Decisions                decisions.Runtime
	Events                   *eventing.Runtime
	Settings                 configuration.Runtime
	Resources                ResourceTracker
	GitMgr                   *git.Manager
	GitStatusCache           *git.StatusCache
	WorkersCfg               worker.WorkersConfig
	TestSecretMatcher        *secretmatch.Matcher
	DecodeCompleteLeg        workertools.CompleteLegDecoder
	EnsureSecretCapabilities func() error
}

// New creates an uninitialized boards Runtime bound to the given dependencies.
func New(deps Dependencies) *Runtime {
	return &Runtime{
		deps: deps,
	}
}

// WireBoardAndResearch wires the repository provider, board snapshot builder, and web research subsystems.
func (r *Runtime) WireBoardAndResearch(ctx context.Context) error {
	var err error
	r.RepoProvider = repoinfo.NewProvider(sourcecatalog.Process().Trees, r.repoCatalogRoot, filepath.Join(enginepaths.RepoOrientationRootUnder(r.deps.Storage.Directory), "v1"))
	provider := r.RepoProvider
	r.deps.Resources.Track("repo-provider", 70, func(context.Context) error { return provider.Close() })
	r.deps.Resources.Track("source-catalog", 86, sourcecatalog.Process().Drain)
	r.deps.Sessions.Manager.SetRepoProvider(r.RepoProvider)
	r.deps.Delegations.SetRepoProvider(r.RepoProvider)

	r.RepoProvider.SetOnSettled(func(projectDir string) {
		if r.deps.Events.Publisher != nil {
			r.deps.Events.Publisher.PublishBoardForRoot(context.Background(), projectDir)
		}
	})

	scopeCfg, scopeErr := toolscope.Load()
	if scopeErr != nil {
		return fmt.Errorf("toolscope: %w", scopeErr)
	}
	r.deps.Execution.Host.Survey.SetScopeGuards(scopeCfg, r.repoCatalogFileCount)

	r.Snapshot = &board.SnapshotBuilder{
		Delegations:            r.deps.Delegations.Store,
		Workers:                r.deps.Delegations.Queue,
		Workflow:               &board.WorkflowRunSource{Runs: r.deps.Workflows.Manager.Store.Runs, Presentation: r.deps.Workflows.Manager.Presentation},
		Repo:                   r.RepoProvider,
		Git:                    r.deps.GitMgr,
		StatusCache:            r.deps.GitStatusCache,
		RepoSets:               git.NewRepoSetCache(git.DefaultStatusCacheTTL),
		Projects:               r.deps.Storage.Projects,
		Scans:                  r.deps.Scanning.Coordinator,
		ScanCompare:            r.deps.Scanning.Coordinator,
		SecurityScanners:       r.deps.Settings.Service.SecurityScanners,
		OverlayGate:            r.deps.Settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, r.deps.Storage.Projects),
		DefaultExecutionTarget: worker.DefaultExecutionTarget(r.deps.WorkersCfg),
		Cost:                   r.deps.Providers.Costs,
		CostTrackingEnabled: func() bool {
			return r.deps.Settings.Service != nil && r.deps.Settings.Service.Pricing != nil && r.deps.Settings.Service.Pricing.Effective().CostTrackingEnabled
		},
		Worktree: r.deps.Sessions.Manager.BoardGitWorktreeFunc(r.deps.GitMgr),
	}

	r.deps.Sessions.Manager.SetBoardInject(&board.InjectBuilder{SnapshotBuilder: r.Snapshot, Projects: r.deps.Storage.Projects}, board.DefaultInjectFormatter())
	r.deps.Sessions.Manager.SetIncludeScanLegend(func() bool {
		if r.deps.Settings.Service == nil || r.deps.Settings.Service.SecurityScanners == nil {
			return true
		}
		return r.deps.Settings.Service.SecurityScanners.Effective().Enabled
	})

	if err := board.RegisterBoardTools(r.deps.Execution.Host.Registry, board.ToolDeps{
		Builder:            r.Snapshot,
		Findings:           func() findings.Store { return r.Findings },
		RootSession:        r.rootSessionKey,
		PromotePaths:       r.deps.Sessions.Manager.PromotePathBoardLines,
		OverlayMergePlan:   r.deps.Sessions.Manager.OverlayMergePlanFn(),
		ActiveReservations: r.deps.Sessions.Manager.ActiveReservationBoardEntries,
	}); err != nil {
		return fmt.Errorf("board tools: %w", err)
	}

	r.deps.Execution.Host.Survey.SetListDirUnionBrief(func(ctx context.Context, tctx tools.ToolContext, subpath string) (string, error) {
		if len(tctx.Source.Roots) < 2 {
			return "", nil
		}
		if subpath != "" && !projectroot.IsUnionDiscoveryPath(subpath) {
			return "", nil
		}
		mrb, err := repoinfo.AnalyzeRoots(ctx, tctx.Source.Roots, repoinfo.DefaultBriefBudget(), r.RepoProvider)
		if err != nil {
			return "", err
		}
		return repoinfo.FormatOrientationBriefText(mrb.OrientationRoots()), nil
	})

	r.deps.Sessions.Manager.SetTurnLoads(r.deps.Execution.TurnLoads)
	r.deps.Sessions.Manager.SetDecider(r.deps.Decisions.Decider)
	r.deps.Sessions.Manager.SetSkillBodyRenderer(r.deps.Execution.Host.Skills.RenderSkillBody)

	r.WebRuntime, err = webresearch.WireRuntime()
	if err != nil {
		return fmt.Errorf("web research runtime: %w", err)
	}
	r.WebCreds = r.WebRuntime.Creds
	webCat, webCfg, webReg := r.WebRuntime.Catalog, r.WebRuntime.Config, r.WebRuntime.Registry
	llmReg, llmPol := r.deps.Providers.RegistryPolicy()
	r.WebDiscoverer = webresearch.NewDirectDiscovererFactory(r.deps.Storage.WebIndex, webReg, r.WebCreds, webCfg, webCat, r.deps.Decisions.Rerank)
	r.deps.Execution.Host.Web.SetDirectDiscovererFactory(r.WebDiscoverer)

	if r.deps.Storage.WebIndex != nil {
		webReg.AttachQuotaStore(r.deps.Storage.WebIndex)
		r.WebWarmer = webresearch.NewWarmer(r.deps.Storage.WebIndex, llmReg, llmPol, webCfg)
		warmer := r.WebWarmer
		r.deps.Resources.Track("web-warmer", 90, func(context.Context) error { warmer.Close(); return nil })
		r.WebWarmer.Cost = r.deps.Providers.Costs
		if r.deps.Providers.Service != nil {
			r.WebWarmer.Plane = r.deps.Providers.Service.Utility
		}
		r.deps.Sessions.Manager.SetIndexWarmer(r.WebWarmer)
		r.WarmRunner = &webresearch.WarmRunner{
			W: r.WebWarmer, Roots: r.ProjectRootPaths, ProjectIDForRoot: r.projectIDForRoot, Repo: r.RepoProvider,
			Live: func() bool { return r.deps.Events.Presence.Live() },
		}
	}

	deps := webresearch.ToolDeps{
		Creds:    r.WebCreds,
		Config:   webCfg,
		Catalog:  webCat,
		Registry: webReg,
		Index:    r.deps.Storage.WebIndex,
		Rerank:   r.deps.Decisions.Rerank,
		Boundary: r.deps.Execution.Host.Boundary,
		SearchWarmHook: func(ctx context.Context, sessionID, toolCallID, query, projectDir string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool) {
			r.deps.Sessions.Manager.WarmIndexForSearch(ctx, sessionID, toolCallID, query, projectDir, hitURLs, residualURLs, strongHits, maxResults, directParticipated)
		},
		FetchWarmHook: func(ctx context.Context, sessionID, toolCallID, pageURL, title, projectDir string) {
			r.deps.Sessions.Manager.WarmIndexForFetch(ctx, sessionID, toolCallID, pageURL, title, projectDir)
		},
	}

	if matcher, err := r.deps.Security.LoadMatcher(r.deps.TestSecretMatcher); err != nil {
		return err
	} else {
		deps.SecretMatcher = matcher
		deps.SecretAsk = r.deps.Security.Ask(r.deps.Execution.Host.Executor.Secrets, r.deps.Execution.Host.Authority.ApprovalsDisabled)
		deps.VisualStore = r.Visual
		deps.VisualScreen = visualscreen.NewGate(visualscreen.NewScanner(nil).WithRenderedReferences(browser.RenderLoadsReference), matcher, deps.SecretAsk)
		if r.deps.EnsureSecretCapabilities != nil {
			if err := r.deps.EnsureSecretCapabilities(); err != nil {
				return err
			}
		}
	}

	if err := webresearch.RegisterToolsWithFactory(r.deps.Execution.Host.Registry, deps, r.deps.Execution.Host.Web.DirectFactoryGetter()); err != nil {
		return fmt.Errorf("web research tools: %w", err)
	}
	if err := httpaction.Register(r.deps.Execution.Host.Registry, httpaction.Deps{
		Boundary: r.deps.Execution.Host.Boundary, SecretMatcher: deps.SecretMatcher, SecretAsk: deps.SecretAsk,
		Secrets: r.deps.Security.Capabilities,
	}); err != nil {
		return fmt.Errorf("http request tool: %w", err)
	}

	r.deps.Execution.Host.Web.SetWebResearchConfig(webCfg)
	r.deps.Sessions.Manager.SetWebResearchConfig(webCfg)
	return nil
}

func (r *Runtime) repoCatalogFileCount(projectDir string) (int, bool) {
	ctx := context.Background()
	identity, ok, err := r.repoCatalogRoot(ctx, projectDir)
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

func (r *Runtime) repoCatalogRoot(ctx context.Context, rootPath string) (repoinfo.CatalogRoot, bool, error) {
	if r.deps.Storage.Projects == nil {
		return repoinfo.CatalogRoot{}, false, nil
	}
	projects, err := r.deps.Storage.Projects.List(ctx)
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

func (r *Runtime) projectIDForRoot(ctx context.Context, rootPath string) (string, error) {
	if r.deps.Storage.Projects == nil {
		return "", nil
	}
	projects, err := r.deps.Storage.Projects.List(ctx)
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

func (r *Runtime) ProjectRootPaths(ctx context.Context) ([]string, error) {
	if r.deps.Storage.Projects == nil {
		return nil, nil
	}
	projects, err := r.deps.Storage.Projects.List(ctx)
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

func (r *Runtime) rootSessionKey(ctx context.Context, sessionID string) string {
	return session.RootSessionID(ctx, r.deps.Storage.Sessions, sessionID)
}
