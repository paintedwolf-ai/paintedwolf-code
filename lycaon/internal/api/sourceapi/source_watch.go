package sourceapi

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type sourceWatchJob struct {
	requestedGeneration int
}

// CatalogSnapshotFunc reads a complete directory list for watch seeding.
type CatalogSnapshotFunc func(ctx context.Context, projectID string, roots []sourcecatalog.Root) (sourcecatalog.Snapshot, error)

func (s *Handler) ScheduleSourceWatch(parent context.Context, projectID string) {
	if projectID == "" {
		return
	}
	p, err := s.ProjectRegistry.Get(parent, projectID)
	if err != nil || p == nil {
		return
	}
	s.sourceWatchMu.Lock()
	if s.sourceWatchJobs == nil {
		s.sourceWatchJobs = make(map[string]*sourceWatchJob)
	}
	if active := s.sourceWatchJobs[projectID]; active != nil {
		if p.RootsGeneration > active.requestedGeneration {
			active.requestedGeneration = p.RootsGeneration
		}
		s.sourceWatchMu.Unlock()
		return
	}
	job := &sourceWatchJob{requestedGeneration: p.RootsGeneration}
	s.sourceWatchJobs[projectID] = job
	s.sourceWatchMu.Unlock()
	s.background.Go(parent, func(ctx context.Context) {
		for {
			appliedGeneration, exists := s.EnsureSourceWatch(ctx, projectID)
			s.sourceWatchMu.Lock()
			settled := !exists || job.requestedGeneration <= appliedGeneration
			if settled {
				delete(s.sourceWatchJobs, projectID)
			}
			s.sourceWatchMu.Unlock()
			if settled {
				return
			}
		}
	})
}

// Bind the watcher before the catalog walk begins.
func (s *Handler) EnsureSourceWatch(ctx context.Context, projectID string) (int, bool) {
	p, err := s.ProjectRegistry.Get(ctx, projectID)
	if err != nil || p == nil {
		return 0, false
	}
	s.ensureWorkspaceWatch(ctx, p)
	return p.RootsGeneration, true
}

func (s *Handler) ensureWorkspaceWatch(ctx context.Context, p *project.Project) bool {
	roots := make([]sourcefeed.RootSpec, 0, len(p.Roots))
	for _, r := range p.Roots {
		roots = append(roots, sourcefeed.RootSpec{ID: r.ID, WorkspaceID: p.WorkspaceID(), Path: r.Path})
	}
	bound := sourcefeed.EnsureProjectWatch(ctx, p.ID, p.SourceBranch.String(), roots, func(ctx context.Context, _ string, batch sourcefeed.ExternalBatch) {
		s.observeWorkspaceChanges(ctx, p, batch)
	})
	s.sourceWatchMu.Lock()
	if s.watchedProjects == nil {
		s.watchedProjects = make(map[string]struct{})
	}
	s.watchedProjects[p.ID] = struct{}{}
	s.sourceWatchMu.Unlock()
	if bound {
		s.scheduleWorkspaceInventory(ctx, p)
	}
	s.seedSourceWatch(ctx, p)
	return bound
}

// StopSourceWatches unbinds every project watch this host bound. The watch
// registry is process-wide and its observers reach this handler, so a closed
// host must leave none behind.
func (s *Handler) StopSourceWatches(ctx context.Context) {
	s.sourceWatchMu.Lock()
	projects := make([]string, 0, len(s.watchedProjects))
	for projectID := range s.watchedProjects {
		projects = append(projects, projectID)
	}
	clear(s.watchedProjects)
	s.sourceWatchMu.Unlock()
	for _, projectID := range projects {
		sourcefeed.StopProjectWatch(ctx, projectID)
	}
}

// Platforms with per-directory watches need the catalog to complete coverage.
func (s *Handler) seedSourceWatch(ctx context.Context, p *project.Project) {
	needsSeed := s.WatchNeedsSeed
	if needsSeed == nil {
		needsSeed = sourcefeed.WatchNeedsSeed
	}
	var pending []project.Root
	for _, r := range p.Roots {
		if needsSeed(r.Path) {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return
	}
	catalogRoots := make([]sourcecatalog.Root, 0, len(pending))
	for _, r := range pending {
		catalogRoots = append(catalogRoots, sourcecatalog.Root{ID: r.ID, Path: r.Path})
	}
	snapshot := s.CatalogSnapshot
	if snapshot == nil {
		snapshot = sourcecatalog.Process().Snapshot
	}
	// Seed each root once with its complete directory list.
	catalog, err := snapshot(ctx, p.ID, catalogRoots)
	if err != nil {
		return
	}
	seeds := make([]sourcefeed.RootSeed, 0, len(pending))
	for _, r := range pending {
		seed := sourcefeed.RootSeed{Path: r.Path}
		for _, dir := range catalog.Directories(r.ID, ".") {
			entries, _ := catalog.Listing(r.ID, dir.Path)
			seed.Directories = append(seed.Directories, sourcefeed.DirectorySpec{
				Path: filepath.Join(r.Path, filepath.FromSlash(dir.Path)), EntryCount: len(entries),
			})
		}
		seeds = append(seeds, seed)
	}
	sourcefeed.SeedProjectWatch(ctx, p.ID, seeds)
}

// observeWorkspaceChanges reconciles a batch before the watcher publishes it:
// the ledger records the named paths, then open documents fold in the bytes.
// A window the watcher could not name needs the full inventory pass, which
// runs on its own.
func (s *Handler) observeWorkspaceChanges(ctx context.Context, p *project.Project, batch sourcefeed.ExternalBatch) {
	if batch.Resync || batch.HeadMoved || len(batch.Changes) == 0 {
		s.scheduleWorkspaceInventory(ctx, p)
	} else {
		s.observeLedgerPaths(ctx, p, batch)
	}
	s.RevalidateEditorDocuments(ctx, p, batch)
}

func (s *Handler) observeLedgerPaths(ctx context.Context, p *project.Project, batch sourcefeed.ExternalBatch) {
	if s.SourceInventory == nil {
		return
	}
	refs := make([]sourceledger.PathRef, 0, len(batch.Changes))
	for _, change := range batch.Changes {
		refs = append(refs, sourceledger.PathRef{RootID: change.RootID, Path: change.Path})
	}
	if len(refs) == 0 {
		return
	}
	req := sourceInventoryRequest(p)
	if _, err := s.SourceInventory.ObservePaths(ctx, p.ID, req.Roots, refs); err != nil {
		s.responses.Logger.WarnContext(ctx, "source ledger observe paths", "project_id", p.ID, "err", err)
	}
}

// RevalidateEditorDocuments reconciles the open documents a batch names, or
// every open document when it names none.
func (s *Handler) RevalidateEditorDocuments(ctx context.Context, p *project.Project, batch sourcefeed.ExternalBatch) {
	var selection []editordoc.PathRef
	if !batch.Resync && !batch.HeadMoved {
		selection = make([]editordoc.PathRef, 0, len(batch.Changes))
		for _, change := range batch.Changes {
			selection = append(selection, editordoc.PathRef{RootID: change.RootID, Path: change.Path})
		}
		if len(selection) == 0 {
			return
		}
	}
	if err := s.EditorDocuments.ObserveExternal(ctx, p, selection); err != nil {
		s.responses.Logger.WarnContext(ctx, "editor documents observe external", "project_id", p.ID, "err", err)
	}
}
