package sourceapi

import (
	"context"
	"reflect"

	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (view *sourceView) outsideLocations() []wire.SourceChatLocation {
	view.mu.Lock()
	defer view.mu.Unlock()
	return view.navigation.outside
}

// selectTreeChat replaces the appended roots under the current intent journal.
func (s *Trees) selectTreeChat(ctx context.Context, view *sourceView, chatID string) (*sourcetree.Filtered, error) {
	project, err := s.ProjectRegistry.Get(ctx, view.scope.Project)
	if err != nil {
		return nil, err
	}
	project, err = s.Views.sourceViewProject(ctx, project, view)
	if err != nil {
		return nil, err
	}
	locations, err := s.chatLocations(ctx, view.scope, project, chatID)
	if err != nil {
		return nil, err
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if reflect.DeepEqual(view.navigation.outside, locations.details) {
		view.navigation.treeIntent.ChatID = chatID
		return nil, nil
	}
	if _, err := view.navigation.tree.ReplaceOnDemandRoots(ctx, locations.roots); err != nil {
		return nil, err
	}
	view.navigation.treeIntent.ChatID = chatID
	view.navigation.outside = locations.details
	if view.navigation.treeIntent.Review != nil {
		return view.changeTreeReview(view.navigation.treeIntent.Review), nil
	}
	return view.changeTreeFilter(view.navigation.treeIntent.Filter), nil
}
