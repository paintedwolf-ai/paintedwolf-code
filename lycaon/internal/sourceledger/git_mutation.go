package sourceledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

// GitMutationContext binds managed repository effects to their invoking turn.
// Watchers share the observation lock, so they cannot consume the movement first.
func (s *Store) GitMutationContext(ctx context.Context, projectID string, roots []RootSpec, actor Contributor) context.Context {
	if s == nil || s.gitReader == nil {
		return ctx
	}
	return gitstate.WithMutationObserver(ctx, func(ctx context.Context, dir string, paths []string) (func(context.Context) error, error) {
		for _, root := range roots {
			if gitrepo.CanonicalDir(dir) == gitrepo.CanonicalDir(root.Path) {
				return s.beginGitMutation(ctx, projectID, roots, root, paths, actor)
			}
		}
		return nil, fmt.Errorf("the Git mutation has no attached source root")
	})
}

func (s *Store) beginGitMutation(ctx context.Context, projectID string, roots []RootSpec, root RootSpec, paths []string, actor Contributor) (func(context.Context) error, error) {
	release, err := s.gitObservations.Acquire(ctx, gitObservationKey(projectID, root))
	if err != nil {
		return nil, err
	}
	// Existing drift belongs to its previous observer, never this invocation.
	prior, err := s.observeRootGitState(ctx, projectID, root, gitAttribution{})
	if err != nil {
		release()
		return nil, err
	}
	if prior != "" {
		if err := sourcefeed.EmitGitSignal(ctx, projectID, rootRefsOf(roots)); err != nil {
			release()
			return nil, err
		}
	}
	refs := make([]PathRef, 0, len(paths))
	for _, path := range paths {
		refs = append(refs, PathRef{RootID: root.ID, Path: path})
	}
	if _, err := s.observePaths(ctx, projectID, roots, refs, map[string]string{root.ID: prior}, nil); err != nil {
		release()
		return nil, err
	}
	return func(ctx context.Context) error {
		defer release()
		observation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		id, gitErr := s.observeRootGitState(observation, projectID, root, gitAttribution{actor: actor})
		_, pathErr := s.observePaths(observation, projectID, roots, refs, map[string]string{root.ID: id}, &actor)
		var signalErr error
		if id != "" {
			signalErr = sourcefeed.EmitGitSignal(observation, projectID, rootRefsOf(roots))
		}
		return errors.Join(gitErr, pathErr, signalErr)
	}, nil
}

func gitObservationKey(projectID string, root RootSpec) string {
	return projectID + "\x00" + root.BranchID.String() + "\x00" + root.ID
}

type gitAttribution struct {
	actor           Contributor
	commandWindowID string
}

// Stable ordering prevents multi-root inventory from deadlocking a path observer.
func (s *Store) lockObservations(ctx context.Context, projectID string, roots []RootSpec) (func(), error) {
	keys := make([]string, 0, len(roots))
	for _, root := range roots {
		keys = append(keys, gitObservationKey(projectID, root))
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	releases := make([]func(), 0, len(keys))
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, key := range keys {
		unlock, err := s.gitObservations.Acquire(ctx, key)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	return release, nil
}
