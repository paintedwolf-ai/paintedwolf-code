package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func viewProjectionRevision(intent, projection string) string {
	sum := sha256.Sum256([]byte(intent + "\x00" + projection))
	return hex.EncodeToString(sum[:])
}

func treeAddress(address wire.SourceTreeAddress) sourcetree.Address {
	return sourcetree.Address{Root: address.RootID, Path: address.Path}
}
func wireTreeAddress(address sourcetree.Address) wire.SourceTreeAddress {
	return wire.SourceTreeAddress{RootID: address.Root, Path: address.Path}
}
func wireViewExtent(extent pagedview.Extent) wire.SourceViewExtent {
	return wire.SourceViewExtent{Rows: extent.Rows, Complete: extent.Complete}
}

func (view *sourceView) snapshot(ctx context.Context) (wire.SourceView, error) {
	for {
		if err := ctx.Err(); err != nil {
			return wire.SourceView{}, err
		}
		read, release := view.read()
		state, err := read.snapshot(ctx)
		if err == nil {
			err = read.validate()
		}
		release()
		if !errors.Is(err, pagedview.ErrRevision) {
			return state, err
		}
	}
}

func (view *sourceViewRead) snapshot(ctx context.Context) (wire.SourceView, error) {
	intentRevision := view.intentRevision
	if view.tree == nil {
		extent := wire.SourceViewExtent{Complete: view.state == "ready"}
		if view.projection != nil {
			rows, err := view.projection.Extent(ctx)
			if err != nil {
				return wire.SourceView{}, err
			}
			extent.Rows = rows
		}
		return wire.SourceView{Comparison: &wire.SourceComparisonView{Kind: "comparison", ID: view.id,
			IntentRevision: intentRevision, ProjectionRevision: viewProjectionRevision(intentRevision, view.projectionRevision),
			State: view.state, Extent: extent, ExpiresAt: view.expires, Failure: view.failure,
			Intent: view.comparisonIntent, Comparison: view.details}}, nil
	}
	revision := pagedview.Revision{Projection: view.projectionRevision}
	extent := pagedview.Extent{}
	var err error
	if !view.reviewPreparing {
		if view.presentation != nil {
			revision, extent = view.presentation.Revision()
		} else {
			revision, extent, err = view.tree.Revision(ctx)
		}
		var preparation *sourcetree.PreparationError
		if errors.As(err, &preparation) {
			view.state = "failed"
			view.failure = sourcePreparationFailure(preparation, "The file tree could not be prepared.")
			err = nil
		}
		if errors.Is(err, pagedview.ErrPreparing) {
			err = nil
			view.state = "preparing"
			revision = pagedview.Revision{Projection: view.projectionRevision}
		}
	}
	if view.treeIntent.Filter != "" {
		if view.filtered != nil {
			revision, extent = view.filtered.Revision()
		} else {
			revision = pagedview.Revision{Projection: view.projectionRevision}
			extent = pagedview.Extent{}
		}
	}
	if err != nil {
		return wire.SourceView{}, err
	}
	intent := view.treeIntent
	if view.treeInitialized {
		intent.Disclosures = make([]wire.SourceTreeDisclosure, 0)
		for _, entry := range view.tree.Intent() {
			disclosure := wire.SourceTreeDisclosure{Address: wireTreeAddress(entry.Address), Open: entry.Disclosure.Open, Recursive: entry.Disclosure.Recursive}

			intent.Disclosures = append(intent.Disclosures, disclosure)
		}
	}
	return wire.SourceView{Tree: &wire.SourceTreeView{Kind: "tree", ID: view.id,
		IntentRevision: intentRevision, ProjectionRevision: revision.Projection,
		State: view.state, Extent: wireViewExtent(extent), ExpiresAt: view.expires, Failure: view.failure,
		WorkspaceID: view.workspaceID, Intent: intent, Roots: view.roots, LoadingDirectories: view.tree.LoadingDirectories()}}, nil
}

// sourceViewPublication identifies what a client can observe; renewing the lease is not a change.
func sourceViewPublication(snapshot wire.SourceView) ([sha256.Size]byte, error) {
	if tree := snapshot.Tree; tree != nil {
		observed := *tree
		observed.ExpiresAt = time.Time{}
		snapshot = wire.SourceView{Tree: &observed}
	} else if comparison := snapshot.Comparison; comparison != nil {
		observed := *comparison
		observed.ExpiresAt = time.Time{}
		snapshot = wire.SourceView{Comparison: &observed}
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(body), nil
}

// Notifications carry observable changes only; notifier delivery is serialized per view,
// so published needs no lock.
func (s *Handler) publishSourceView(view *sourceView) {
	if s.Events == nil || view.ctx.Err() != nil {
		return
	}
	snapshot, err := view.snapshot(view.ctx)
	if err != nil {
		return
	}
	publication, err := sourceViewPublication(snapshot)
	if err != nil || publication == view.published {
		return
	}
	view.published = publication
	event := wire.SourceViewEvent{ViewID: view.id}
	if tree := snapshot.Tree; tree != nil {
		event.Kind, event.IntentRevision, event.ProjectionRevision = "tree", tree.IntentRevision, tree.ProjectionRevision
		event.Terminal = tree.State == "failed"

	} else {
		comparison := snapshot.Comparison
		event.Kind, event.IntentRevision, event.ProjectionRevision = "comparison", comparison.IntentRevision, comparison.ProjectionRevision
		event.Terminal = comparison.State != "preparing"
	}
	_ = s.Events.Publish(view.ctx, wire.EventTopicSourceView, events.PublishKey{Project: view.scope.Project, Facet: view.id}, event)
}

//nolint:contextcheck,nolintlint // Invalidation delivery survives the request and view cancellation.
func (s *Handler) publishSourceViewInvalidated(view *sourceView) {
	if s.Events == nil {
		return
	}
	view.intentMu.RLock()
	view.mu.Lock()
	kind := "comparison"
	if view.tree != nil {
		kind = "tree"
	}
	event := wire.SourceViewEvent{ViewID: view.id, Kind: kind, IntentRevision: view.commands.Revision(), ProjectionRevision: view.projectionRevision, Terminal: true, Invalidated: true}
	view.mu.Unlock()
	view.intentMu.RUnlock()
	_ = s.Events.Publish(s.background.Context(), wire.EventTopicSourceView, events.PublishKey{Project: view.scope.Project, Facet: view.id}, event)
}

const sourceStorageFullMessage = "There is not enough disk space to prepare the source view. Free disk space or clear unused caches in Settings, then retry."
const sourceViewCapacityMessage = "Source presentation capacity is busy. Close an unused view and try again."

func sourcePreparationFailure(err error, message string) *wire.SourceViewFailure {
	code := "preparation_failed"
	var failure *comparisonFailure
	switch {
	case pagedview.StorageFull(err):
		code, message = "resource_limit", sourceStorageFullMessage
	case errors.Is(err, pagedview.ErrBudget):
		code, message = "resource_limit", sourceViewCapacityMessage
	case errors.Is(err, pagedview.ErrExpired):
		code, message = "expired", "The retained source presentation expired. Reopen it to continue."
	case errors.Is(err, store.ErrSessionNotFound):
		code, message = "unavailable", "This chat no longer exists."
	case errors.Is(err, sourcetree.ErrUnknownRoot):
		code, message = "unavailable", "That folder is not attached to this project."
	case errors.As(err, &failure):
		code, message = "unavailable", failure.message
	}
	return &wire.SourceViewFailure{Code: code, Message: message}
}
