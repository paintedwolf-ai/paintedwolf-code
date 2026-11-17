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

func (s *Trees) newTreeView(scope pagedview.Scope, p *project.Project, request wire.SourceTreeViewCreate) *sourceView {
	ctx, cancel := context.WithCancel(s.background.Context())
	view := &sourceView{scope: scope, clientID: request.ClientID, sessionID: request.SessionID, workspaceID: p.WorkspaceID(),
		ctx: ctx, cancel: cancel, state: "preparing", commands: pagedview.NewCommands[string](&s.sourceViews.receipts), expires: time.Now().Add(sourceViewLifetime), projectionRevision: uuid.NewString(), navigation: sourceViewNavigation{
			treeIntent: request.Intent, roots: project.ToAPI(p).Roots}, filtering: sourceViewFiltering{
			filterGeneration: uuid.NewString()}, reviewing: sourceViewReviewing{reviewGeneration: uuid.NewString(), reviewPreparing: true, reviewDirty: true}}
	roots := make([]sourcetree.Root, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, sourcetree.Root{Root: sourcecatalog.Root{ID: root.ID, Path: root.Path}, Label: root.Label})
	}
	view.notifier = pagedview.NewNotifier(250*time.Millisecond, func() { s.Views.publishSourceView(view) })
	for _, root := range roots {
		if err := sourcecatalog.Process().Directories.WarmNavigation(ctx, scope.Project, root.Root); err != nil {
			slog.Warn("Source inventory could not start", "root", root.ID, "error", err)
		}
	}
	view.navigation.outside = s.chatLocations(ctx, p, request.ChatID)
	for _, location := range view.navigation.outside {
		roots = append(roots, chatLocationRoot(location))
	}
	view.navigation.treeIntent.Disclosures = disclosuresWithin(view.navigation.treeIntent.Disclosures, roots)
	view.navigation.tree = sourcetree.New(ctx, scope, roots, sourcecatalog.Process(), func() { s.treeViewChanged(view) })
	s.watchTreeView(view, p)
	return view
}

func (s *Trees) prepareTreeView(view *sourceView, release func()) {
	s.background.Go(view.ctx, func(ctx context.Context) {
		defer release()
		view.intentMu.Lock()
		if err := view.navigation.tree.Disclose(ctx, nil, treeDisclosures(view.navigation.treeIntent.Disclosures)...); err != nil {
			view.intentMu.Unlock()
			view.fail(err)
			view.notifier.Notify(true)
			return
		}
		view.navigation.treeInitialized = true
		done := view.navigation.tree.Prepare()
		view.intentMu.Unlock()
		select {
		case <-done:
		case <-view.ctx.Done():
			return
		}
		view.mu.Lock()
		view.navigation.treePrepared = true
		view.mu.Unlock()
		s.refreshTreeReview(view)
		view.notifier.Notify(true)
	})
}

func (s *Trees) retainSourceWork(view *sourceView, done <-chan struct{}) {
	_, release, err := s.Views.sourceViewRegistry().registry.Acquire(view.scope, view.id)
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

// disclosuresWithin keeps the saved disclosures of roots this view lists;
// another chat's locations share the workspace's saved intent.
func disclosuresWithin(disclosures []wire.SourceTreeDisclosure, roots []sourcetree.Root) []wire.SourceTreeDisclosure {
	listed := make(map[string]bool, len(roots))
	for _, root := range roots {
		listed[root.ID] = true
	}
	kept := disclosures[:0:0]
	for _, disclosure := range disclosures {
		if listed[disclosure.Address.RootID] {
			kept = append(kept, disclosure)
		}
	}
	return kept
}
