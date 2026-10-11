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
	id                 string
	workspaceID        string
	state              string
	intentRevision     string
	projectionRevision string
	scope              pagedview.Scope
	presentation       *sourcetree.Presentation
	failure            *wire.SourceViewFailure
	currentIntent      func() string
	expires            time.Time
	navigation         sourceReadNavigation
	filtering          sourceReadFiltering
	reviewing          sourceReadReviewing
	comparisonData     sourceReadComparisonData
}

type sourceReadNavigation struct {
	tree            *sourcetree.View
	treeInitialized bool
	treeIntent      wire.SourceTreeIntent
	roots           []wire.ProjectRoot
}

type sourceReadFiltering struct {
	filtered *sourcetree.Filtered
}

type sourceReadReviewing struct {
	reviewPreparing bool
}

type sourceReadComparisonData struct {
	comparisonIntent wire.SourceComparisonIntent
	comparison       *sourcecomparison.Document
	current          *currentSourceSnapshot
	projection       *sourceViewProjection
	details          *wire.SourceComparisonDetails
}

func (view *sourceView) read() (*sourceViewRead, func()) {
	view.intentMu.RLock()
	view.mu.Lock()
	read := &sourceViewRead{
		scope: view.scope, id: view.id, workspaceID: view.workspaceID, state: view.state,
		intentRevision: view.commands.Revision(), projectionRevision: view.projectionRevision, failure: view.failure, expires: view.expires, navigation: sourceReadNavigation{
			tree: view.navigation.tree, treeInitialized: view.navigation.treeInitialized, treeIntent: view.navigation.treeIntent, roots: view.navigation.roots}, filtering: sourceReadFiltering{filtered: view.filtering.filtered}, reviewing: sourceReadReviewing{
			reviewPreparing: view.reviewing.reviewPreparing}, comparisonData: sourceReadComparisonData{
			comparisonIntent: view.comparisonData.comparisonIntent, comparison: view.comparisonData.comparison, current: view.comparisonData.current,
			projection: view.comparisonData.projection, details: view.comparisonData.details}}
	var releaseFilter func()
	if read.filtering.filtered != nil {
		releaseFilter = read.filtering.filtered.Retain()
	}
	if read.comparisonData.projection != nil {
		read.comparisonData.projection.retain()
	}
	read.currentIntent = view.commands.Revision
	view.mu.Unlock()
	view.intentMu.RUnlock()
	return read, func() {
		read.comparisonData.projection.release()
		if releaseFilter != nil {
			releaseFilter()
		}
	}
}

// Tree snapshots validate intent after commands that race the frame read.
func (read *sourceViewRead) validate() error {
	if read.state == "ready" && read.comparisonData.current != nil && !read.comparisonData.current.stream.Current() {
		return currentSourceChanged()
	}
	if read.navigation.tree != nil && read.currentIntent() != read.intentRevision {
		return pagedview.ErrRevision
	}
	return nil
}
