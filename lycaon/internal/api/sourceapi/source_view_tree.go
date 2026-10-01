package sourceapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) newTreeView(scope pagedview.Scope, p *project.Project, request wire.SourceTreeViewCreate) *sourceView {
	ctx, cancel := context.WithCancel(s.background.Context())
	view := &sourceView{scope: scope, clientID: request.ClientID, sessionID: request.SessionID, workspaceID: p.WorkspaceID(),
		ctx: ctx, cancel: cancel, state: "preparing", commands: pagedview.NewCommands[string](&s.sourceViews.receipts),
		treeIntent: request.Intent, roots: project.ToAPI(p).Roots, expires: time.Now().Add(sourceViewLifetime),
		filterGeneration: uuid.NewString(), projectionRevision: uuid.NewString(), reviewGeneration: uuid.NewString(), reviewPreparing: true, reviewDirty: true}
	roots := make([]sourcetree.Root, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, sourcetree.Root{Root: sourcecatalog.Root{ID: root.ID, Path: root.Path}, Label: root.Label})
	}
	view.notifier = pagedview.NewNotifier(250*time.Millisecond, func() { s.publishSourceView(view) })
	for _, root := range roots {
		if err := sourcecatalog.Process().WarmNavigation(ctx, scope.Project, root.Root); err != nil {
			slog.Warn("Source inventory could not start", "root", root.ID, "error", err)
		}
	}
	view.tree = sourcetree.New(ctx, scope, roots, sourcecatalog.Process(), func() { s.treeViewChanged(view) })
	s.watchTreeView(view, p)
	return view
}

func (s *Handler) prepareTreeView(view *sourceView, release func()) {
	s.background.Go(view.ctx, func(ctx context.Context) {
		defer release()
		view.intentMu.Lock()
		if err := view.tree.Disclose(ctx, nil, treeDisclosures(view.treeIntent.Disclosures)...); err != nil {
			view.intentMu.Unlock()
			view.fail(err)
			view.notifier.Notify(true)
			return
		}
		view.treeInitialized = true
		done := view.tree.Prepare()
		view.intentMu.Unlock()
		select {
		case <-done:
		case <-view.ctx.Done():
			return
		}
		view.mu.Lock()
		view.treePrepared = true
		view.mu.Unlock()
		s.refreshTreeReview(view)
		view.notifier.Notify(true)
	})
}

func (s *Handler) retainSourceWork(view *sourceView, done <-chan struct{}) {
	_, release, err := s.sourceViewRegistry().registry.Acquire(view.scope, view.id)
	if err != nil {
		return
	}
	s.background.Go(view.ctx, func(_ context.Context) {
		defer release()
		select {
		case <-done:
		case <-view.ctx.Done():
			return
		}
		s.refreshTreeFilter(view)
		view.notifier.Notify(true)
	})
}

func treeDisclosures(disclosures []wire.SourceTreeDisclosure) []sourcetree.IntentEntry {
	changes := make([]sourcetree.IntentEntry, len(disclosures))
	for i, disclosure := range disclosures {
		changes[i] = sourcetree.IntentEntry{Address: treeAddress(disclosure.Address),
			Disclosure: sourcetree.Disclosure{Open: disclosure.Open, Recursive: disclosure.Recursive}}
	}
	return changes
}
