package projectsource

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/repochange"
)

type SourcePaths struct {
	stateMu      sync.Mutex
	reservations map[*sourceReservation]struct{}
	historyLocks keylock.Group
}

var ErrSourceBusy = errors.New("source path has a file operation in progress")

// ReserveSourcePath coordinates document publication with lifecycle operations.
func (s *SourcePaths) ReserveSourcePath(root, path string) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	return s.reserveSourcePlan(sourceMutationPlan{Kind: "write", AbsPath: filepath.Join(root, filepath.FromSlash(path))})
}

type sourceReservation struct {
	paths []string
}

// Path reservations span preparation and publication across user and agent mutations.
func (s *SourcePaths) reserveSourcePlan(plan sourceMutationPlan) (func(), error) {
	var paths []string
	var collect func(sourceMutationPlan)
	collect = func(p sourceMutationPlan) {
		if p.AgentEffect != nil {
			for _, loc := range []fseffect.Location{p.AgentEffect.Target, p.AgentEffect.From} {
				if loc.Root != "" && loc.Rel != "" {
					paths = append(paths, canonicalSourceEntry(filepath.Join(loc.Root, loc.Rel)))
				}
			}
		}
		if p.Kind == "batch_write" {
			for _, child := range p.Writes {
				collect(child)
			}
			return
		}
		for _, path := range []string{p.AbsPath, p.FromAbs, p.ToAbs, p.StageAbs, p.HoldAbs} {
			if path != "" {
				paths = append(paths, canonicalSourceEntry(path))
			}
		}
	}
	collect(plan)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for held := range s.reservations {
		for _, a := range held.paths {
			for _, b := range paths {
				if overlappingSourcePaths(a, b) {
					return nil, ErrSourceBusy
				}
			}
		}
	}
	held := &sourceReservation{paths: paths}
	if s.reservations == nil {
		s.reservations = make(map[*sourceReservation]struct{})
	}
	s.reservations[held] = struct{}{}
	var releasePrivate []func()
	for _, path := range []string{plan.StageAbs, plan.HoldAbs} {
		if path != "" {
			releasePrivate = append(releasePrivate, repochange.HoldPrivateTree(path))
		}
	}
	return func() {
		s.stateMu.Lock()
		delete(s.reservations, held)
		s.stateMu.Unlock()
		for _, release := range releasePrivate {
			release()
		}
	}, nil
}

func overlappingSourcePaths(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func canonicalSourceEntry(path string) string {
	return filepath.ToSlash(filepath.Join(fspath.CanonicalPath(filepath.Dir(path)), filepath.Base(path)))
}

// Lifecycle history is ordered independently of ordinary document writes.
func (s *SourcePaths) lockSourceHistory(ctx context.Context, projectID string) (func(), error) {
	return s.historyLocks.Acquire(ctx, projectID)
}
