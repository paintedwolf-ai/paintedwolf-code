package sourceapi

import (
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// sourceViewRead pins a publication; frame work runs outside the progress lock.
type sourceViewRead struct {
	id, workspaceID, state, intentRevision, projectionRevision string
	scope                                                      pagedview.Scope
	presentation                                               *sourcetree.Presentation
	tree                                                       *sourcetree.View
	filtered                                                   *sourcetree.Filtered
	treeInitialized, reviewPreparing                           bool
	treeIntent                                                 wire.SourceTreeIntent
	roots                                                      []wire.ProjectRoot
	comparisonIntent                                           wire.SourceComparisonIntent
	comparison                                                 *sourcecomparison.Document
	current                                                    *currentSourceSnapshot
	projection                                                 *sourceViewProjection
	details                                                    *wire.SourceComparisonDetails
	failure                                                    *wire.SourceViewFailure
	currentIntent                                              func() string
	expires                                                    time.Time
}

func (view *sourceView) read() (*sourceViewRead, func()) {
	view.intentMu.RLock()
	view.mu.Lock()
	read := &sourceViewRead{
		scope: view.scope, id: view.id, workspaceID: view.workspaceID, state: view.state,
		intentRevision: view.commands.Revision(), projectionRevision: view.projectionRevision,
		tree: view.tree, filtered: view.filtered, treeInitialized: view.treeInitialized,
		reviewPreparing: view.reviewPreparing, treeIntent: view.treeIntent, roots: view.roots,
		comparisonIntent: view.comparisonIntent, comparison: view.comparison, current: view.current,
		projection: view.projection, details: view.details, failure: view.failure, expires: view.expires,
	}
	var releaseFilter func()
	if read.filtered != nil {
		releaseFilter = read.filtered.Retain()
	}
	if read.projection != nil {
		read.projection.retain()
	}
	read.currentIntent = view.commands.Revision
	view.mu.Unlock()
	view.intentMu.RUnlock()
	return read, func() {
		read.projection.release()
		if releaseFilter != nil {
			releaseFilter()
		}
	}
}

// Tree snapshots validate intent after commands that race the frame read.
func (read *sourceViewRead) validate() error {
	if read.state == "ready" && read.current != nil && !read.current.stream.Current() {
		return currentSourceChanged()
	}
	if read.tree != nil && read.currentIntent() != read.intentRevision {
		return pagedview.ErrRevision
	}
	return nil
}
