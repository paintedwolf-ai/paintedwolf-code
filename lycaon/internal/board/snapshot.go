package board

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/standingpatterns"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MaxBoardGitOthers is the number of non-active repositories carried on BoardGitSlice.
const MaxBoardGitOthers = 4

// WorkerTouchEnricher supplies live touched paths for in-flight worker jobs.
type WorkerTouchEnricher interface {
	Paths(jobID string) []string
}

// ActiveReservationLister returns handoff_reserve holds for a coordinator session.
type ActiveReservationLister func(sessionID string) []api.BoardReservationEntry

type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}
type WorkflowPresentation interface {
	AttachRunUI(context.Context, *api.WorkflowRun) error
}
type WorkflowRunSource struct {
	Runs         WorkflowRuns
	Presentation WorkflowPresentation
}

// ScanSource returns assessment authority visible for canonical paths.
type ScanSource interface {
	LatestAssessmentForPaths(ctx context.Context, canonicalPaths []string) (scan.AssessmentView, error)
}

// ScanComparer resolves baselines and diffs for pack-board auto-regression.
type ScanComparer interface {
	BoardComparison(ctx context.Context, oldScan, newScan api.CodeScan) (*scan.Comparison, error)
}

// SnapshotBuilder assembles BoardSnapshot DTOs from delegation and worker state.
type SnapshotBuilder struct {
	Delegations        delegation.Store
	Workers            worker.WorkerQueue
	Touches            WorkerTouchEnricher
	ActiveReservations ActiveReservationLister
	Workflow           *WorkflowRunSource
	Repo               repoinfo.Provider
	Git                git.GitManager                  // optional; fail-soft
	StatusCache        *git.StatusCache                // optional; shared with the Git status read
	RepoSets           *git.RepoSetCache               // optional; background repository topology
	Projects           project.Registry                // optional; SSE BuildView loads roots for the repo set
	Scans              ScanSource                      // optional; fail-soft
	ScanCompare        ScanComparer                    // optional; fail-soft auto-compare
	SecurityScanners   *settings.SecurityScannersStore // optional; when set, loadScans omitted when scans disabled in Settings
	// OverlayGate controls project standing-pattern overlays. Nil is closed.
	OverlayGate            *settings.ProjectSurfaceGate
	DefaultExecutionTarget api.ExecutionTarget
	Cost                   cost.CostTracker // optional; fills the complete cost summary
	// CostTrackingEnabled gates the live cost chip.
	CostTrackingEnabled func() bool
	// Worktree is nil when worktree bindings are unavailable.
	Worktree func(ctx context.Context, sessionID string) *api.BoardGitWorktree
}

// Build scopes workers and workflows to the session, and repository facts to the workspace.
// Attached roots share one orientation budget.
func (b *SnapshotBuilder) Build(ctx context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel, roots []projectroot.RootRef) (*api.BoardSnapshot, error) {
	buildStart := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if level == "" {
		level = api.BoardDetailLevelCompact
	}
	sessionID = strings.TrimSpace(sessionID)

	var delegations []api.Delegation
	if b.Delegations != nil {
		var err error
		delegations, err = b.Delegations.ListByProject(ctx, projectID, sessionID)
		if err != nil {
			return nil, err
		}
	}

	var tasks []api.WorkerTask
	if b.Workers != nil && sessionID != "" {
		var err error
		tasks, err = b.Workers.ListBySession(ctx, projectID, sessionID)
		if err != nil {
			return nil, err
		}
		if b.Touches != nil {
			for i := range tasks {
				if paths := b.Touches.Paths(tasks[i].ID); len(paths) > 0 {
					tasks[i].TouchedPaths = append([]string(nil), paths...)
				}
			}
		}
	}

	delegationSlice := api.BoardDelegationSlice{
		"delegations": delegations,
	}
	workersSlice := api.BoardWorkersSlice{
		"tasks": tasks,
	}

	var activeRun *api.WorkflowRun
	if b.Workflow != nil && sessionID != "" {
		activeRun, _ = b.Workflow.Runs.ActiveBySession(ctx, sessionID)
		if activeRun != nil {
			_ = b.Workflow.Presentation.AttachRunUI(ctx, activeRun)
		}
	}

	facts, reused, err := b.repositoryFacts(ctx, projectID, workspacePath, roots)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	snap := &api.BoardSnapshot{
		Repo: facts.repo, OrientationRoots: facts.roots, Host: b.loadHost(ctx, facts.repo.Languages),
		Delegation: &delegationSlice, Workers: &workersSlice, ActiveWorkflowRun: activeRun,
		Cost: b.costSlice(ctx, sessionID), DetailLevel: level, Scans: facts.scans,
	}
	if facts.git != nil {
		gitSlice := *facts.git
		if b.Worktree != nil && sessionID != "" {
			gitSlice.Worktree = b.Worktree(ctx, sessionID)
		}
		snap.Git = &gitSlice
	}
	packboard.EnrichSnapshotStandingFlags(snap, facts.flags)
	if b.ActiveReservations != nil && sessionID != "" {
		packboard.EnrichSnapshotReservationsForSession(sessionID, snap, b.ActiveReservations)
	}
	snap.PackContentHash = PackContentHash(*snap, now)
	observability.LogLatency("board_snapshot", "board snapshot built", buildStart,
		"project_id", projectID,
		"workspace_path", workspacePath,
		"session_id", sessionID,
		"detail_level", level,
		"file_count", facts.repo.FileCount,
		"repository_facts_reused", reused,
		"repo_ms", facts.repoMs,
		"git_ms", facts.gitMs,
		"scans_ms", facts.scansMs,
		"git_cache_hit", facts.gitCacheHit,
		"change_signal", string(facts.changeSignal),
		"flags_ms", facts.flagsMs,
	)
	return snap, nil
}

func (b *SnapshotBuilder) loadHost(ctx context.Context, languages []string) *api.BoardHostSlice {
	target := b.DefaultExecutionTarget
	if target == "" {
		target = api.ExecutionTargetLocal
	}
	host := platform.BoardHost(target)
	// Probed toolchains describe this machine, so only a local target reports them.
	if host != nil && len(host.Toolchains) == 0 && host.ExecutionTarget == api.ExecutionTargetLocal {
		host.Toolchains = packboard.Toolchains(ctx, languages)
	}
	return host
}

func (b *SnapshotBuilder) costSlice(ctx context.Context, sessionID string) *api.CostSummary {
	if b.Cost == nil || sessionID == "" || (b.CostTrackingEnabled != nil && !b.CostTrackingEnabled()) {
		return nil
	}
	summary, err := b.Cost.Summary(ctx, api.CostScopeSession, sessionID, "")
	if err != nil {
		return nil
	}
	return &summary
}

func (b *SnapshotBuilder) standingFlags(ctx context.Context, projectDir string) []standingpatterns.FlagCount {
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	cfg, err := standingpatterns.Load(ctx, projectDir)
	if err != nil || len(cfg.Rules) == 0 {
		return nil
	}
	return processStandingFlags.get(ctx, projectDir, cfg.Rules)
}

func (b *SnapshotBuilder) loadScans(ctx context.Context, canonicalPaths []string) *api.BoardScansSlice {
	if b.Scans == nil || len(canonicalPaths) == 0 {
		return nil
	}
	if b.SecurityScanners != nil && !b.SecurityScanners.Effective().Enabled {
		return nil
	}
	view, err := b.Scans.LatestAssessmentForPaths(ctx, canonicalPaths)
	if err != nil || view.LatestAttemptID == "" {
		return nil
	}
	out := &api.BoardScansSlice{
		CurrentAssessment: view.CurrentSummary,
		LatestAttempt:     view.LatestSummary,
	}
	if view.CurrentID == view.LatestAttemptID {
		out.LatestAttempt = nil
	}
	if out.CurrentAssessment != nil && view.PreviousID != "" && b.ScanCompare != nil {
		if responses, ok := compareAssessmentMembers(ctx, b.ScanCompare, view.Previous, view.Current); ok {
			out.Compare = scan.BuildBoardCompareSlice(view.PreviousID, responses...)
		}
	}
	return out
}

func compareAssessmentMembers(ctx context.Context, comparer ScanComparer, previous, current []api.CodeScan) ([]*scan.Comparison, bool) {
	if comparer == nil || len(previous) == 0 || len(current) == 0 {
		return nil, false
	}
	byScanner := make(map[string]api.CodeScan, len(previous))
	for _, baseline := range previous {
		byScanner[baseline.ScannerID] = baseline
	}
	responses := make([]*scan.Comparison, 0, len(current))
	for _, candidate := range current {
		baseline, found := byScanner[candidate.ScannerID]
		if !found {
			return nil, false
		}
		response, err := comparer.BoardComparison(ctx, baseline, candidate)
		if err != nil || response == nil {
			return nil, false
		}
		responses = append(responses, response)
	}
	return responses, len(responses) == len(previous)
}

func (b *SnapshotBuilder) loadGit(ctx context.Context, projectID, projectDir string, roots []projectroot.RootRef) (*api.BoardGitSlice, bool, repochange.Source) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, false, ""
	}
	if b.StatusCache == nil && b.Git == nil {
		return nil, false, ""
	}

	activeRootID := rootIDForPath(roots, projectDir)
	if activeRootID == "" && len(roots) == 0 {
		activeRootID = projectID
	}
	primaryID := ""
	if p, err := projectroot.PrimaryRoot(roots); err == nil {
		primaryID = p.ID
	}
	discoveryRoots := roots
	if len(discoveryRoots) == 0 {
		discoveryRoots = []projectroot.RootRef{{ID: projectID, Path: projectDir, IsPrimary: true}}
	}
	var repos []git.RepoRef
	if b.RepoSets != nil {
		repos, _ = b.RepoSets.PeekOrRevalidate(
			ctx,
			projectID,
			boardRepoSetGeneration(discoveryRoots),
			discoveryRoots,
		)
	} else {
		repos = git.DiscoverRepos(ctx, discoveryRoots)
	}
	if len(roots) > 0 {
		repos = git.OrderRepos(repos, activeRootID, primaryID)
	}

	activeToplevel := projectDir
	activeIdx := -1
	for i, r := range repos {
		if r.Available && r.Toplevel != "" && samePath(r.Toplevel, activeToplevel) {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 && activeRootID != "" {
		for _, repo := range repos {
			if repo.Available || !slices.Contains(repo.RootIDs, activeRootID) {
				continue
			}
			return &api.BoardGitSlice{
				Available: false,
				Label:     repo.Label,
				Others:    []api.BoardGitRepoLine{},
			}, true, ""
		}
	}

	statusDir := activeToplevel
	if activeIdx >= 0 {
		statusDir = repos[activeIdx].Toplevel
	}
	status, cacheHit, changeSignal, err := b.gitStatus(ctx, statusDir)
	if err != nil {
		if errors.Is(err, git.ErrNotRepository) {
			slice := &api.BoardGitSlice{Available: false, Others: []api.BoardGitRepoLine{}}
			if activeIdx >= 0 {
				slice.Label = repos[activeIdx].Label
			}
			return slice, cacheHit, changeSignal
		}
		return nil, cacheHit, changeSignal
	}
	if status == nil {
		return nil, cacheHit, changeSignal
	}

	// Compact snapshots omit recent commits to avoid a git log.
	slice := &api.BoardGitSlice{
		Available:     true,
		Branch:        status.Branch,
		HeadShort:     status.HeadShort,
		Dirty:         status.Dirty,
		StagedCount:   status.StagedCount,
		UnstagedCount: status.UnstagedCount,
		Others:        []api.BoardGitRepoLine{},
	}
	if activeIdx >= 0 {
		slice.RepoID = repos[activeIdx].ID
		slice.Label = repos[activeIdx].Label
	} else if statusDir != "" {
		slice.RepoID = git.DeriveRepoID(statusDir)
		slice.Label = filepath.Base(statusDir)
	}
	if activeIdx < 0 || len(repos) <= 1 {
		return slice, cacheHit, changeSignal
	}
	others := make([]api.BoardGitRepoLine, 0, MaxBoardGitOthers)
	truncated := 0
	for i, r := range repos {
		if i == activeIdx || !r.Available {
			continue
		}
		if len(others) >= MaxBoardGitOthers {
			truncated++
			continue
		}
		line := api.BoardGitRepoLine{RepoID: r.ID, Label: r.Label}
		st, _, _, stErr := b.gitStatus(ctx, r.Toplevel)
		if stErr == nil && st != nil {
			line.Branch = st.Branch
			line.Dirty = st.Dirty
			line.StagedCount = st.StagedCount
			line.UnstagedCount = st.UnstagedCount
		}
		others = append(others, line)
	}
	slice.Others = others
	slice.OthersTruncated = truncated
	return slice, cacheHit, changeSignal
}

func boardRepoSetGeneration(roots []projectroot.RootRef) int {
	hash := fnv.New32a()
	for _, root := range roots {
		_, _ = fmt.Fprintf(
			hash,
			"%s\x00%s\x00%s\x00%t\x00",
			root.ID,
			root.Path,
			root.Label,
			root.IsPrimary,
		)
	}
	return int(hash.Sum32())
}

func (b *SnapshotBuilder) gitStatus(ctx context.Context, dir string) (*git.GitStatus, bool, repochange.Source, error) {
	if b.StatusCache != nil {
		cached, found, e := b.StatusCache.PeekOrRevalidate(ctx, dir)
		if !found {
			return nil, false, "", e
		}
		return cached.Status, cached.CacheHit, cached.ChangeSignal, e
	}
	if b.Git != nil {
		st, err := b.Git.Status(ctx, dir)
		return st, false, "", err
	}
	return nil, false, "", nil
}

func rootIDForPath(roots []projectroot.RootRef, path string) string {
	path = canonicalizePath(path)
	if path == "" {
		return ""
	}
	bestID := ""
	bestLen := -1
	for _, r := range roots {
		rootAbs := canonicalizePath(r.Path)
		if rootAbs == "" {
			continue
		}
		if path == rootAbs || strings.HasPrefix(path, rootAbs+string(filepath.Separator)) {
			if len(rootAbs) > bestLen {
				bestLen = len(rootAbs)
				bestID = r.ID
			}
		}
	}
	return bestID
}

func samePath(a, b string) bool {
	return canonicalizePath(a) == canonicalizePath(b) && canonicalizePath(a) != ""
}

func canonicalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(eval)
	}
	return abs
}

func (b *SnapshotBuilder) loadOrientation(ctx context.Context, workspacePath string, roots []projectroot.RootRef) (*api.RepoBrief, []api.BoardOrientationRoot, bool, error) {
	if len(roots) == 0 {
		repo, ok, err := b.loadRepo(ctx, workspacePath)
		if err != nil {
			return nil, nil, false, err
		}
		return repo, nil, ok, nil
	}
	mrb, err := repoinfo.AnalyzeRoots(ctx, roots, repoinfo.DefaultBriefBudget(), b.Repo)
	if err != nil {
		return nil, nil, false, err
	}
	repo := mrb.PrimaryRepoBrief()
	sections := mrb.OrientationRoots()
	return &repo, sections, mrb.PrimaryMaterialized(), nil
}

func (b *SnapshotBuilder) loadRepo(ctx context.Context, projectDir string) (*api.RepoBrief, bool, error) {
	if b.Repo == nil {
		return nil, false, fmt.Errorf("repo provider not configured")
	}
	brief, err := b.Repo.Brief(ctx, projectDir)
	if err != nil {
		return nil, false, err
	}
	if !brief.Materialized {
		repo := repoinfo.RefreshingBrief()
		return &repo, false, nil
	}
	repo := repoinfo.WireBrief(brief)
	return &repo, true, nil
}

func scanPathsFromRoots(roots []projectroot.RootRef, fallback string) []string {
	if len(roots) > 0 {
		paths := make([]string, 0, len(roots))
		seen := make(map[string]struct{}, len(roots))
		for _, r := range roots {
			p := strings.TrimSpace(r.Path)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
		if len(paths) > 0 {
			return paths
		}
	}
	if p := strings.TrimSpace(fallback); p != "" {
		return []string{p}
	}
	return nil
}
