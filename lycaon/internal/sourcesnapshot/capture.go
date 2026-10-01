package sourcesnapshot

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// Delta capture waits briefly for watcher delivery, bounded under continuous writes.
const (
	deltaQuietGrace = 150 * time.Millisecond
	deltaQuietWait  = 750 * time.Millisecond
)

type rootCapture struct {
	candidate *rootCandidate
	plan      deltaPlan
}

func (s *Store) converge(ctx context.Context, req Request) (Snapshot, error) {
	for _, root := range req.Roots {
		repochange.EnsureRoot(ctx, root.Path)
	}
	storageRelease, err := s.acquireStorage(ctx, false)
	if err != nil {
		return Snapshot{}, err
	}
	defer storageRelease()
	rootRelease, err := s.acquireRootStorage(ctx, req.Roots, true)
	if err != nil {
		return Snapshot{}, err
	}
	defer rootRelease()

	started := time.Now()
	captures := make([]rootCapture, 0, len(req.Roots))
	defer func() {
		for _, capture := range captures {
			capture.candidate.close(context.WithoutCancel(ctx), s)
		}
	}()
	for _, root := range req.Roots {
		var plan deltaPlan
		// Content verification reads every file; incremental capture waits for events.
		if req.Verify == VerifyContent {
			plan = deltaPlan{upTo: s.deltas.sequence()}
		} else {
			repochange.WaitQuiet(ctx, root.Path, deltaQuietGrace, deltaQuietWait)
			plan = s.deltas.begin(root.Path, s.scopeFor(ctx, req, root).Identity())
		}
		candidate, err := s.candidate(ctx, req, root, plan)
		if err != nil {
			return Snapshot{}, err
		}
		captures = append(captures, rootCapture{candidate: candidate, plan: plan})
	}

	stalled := 0
	for pass := 1; pass <= maxConvergePasses; pass++ {
		moved := false
		for i := range captures {
			capture := &captures[i]
			c := capture.candidate
			root := c.root
			switch {
			case c.unstableFiles():
				moved = true
			case !repochange.Coverage(root.Path).Complete:
				// Incomplete watcher coverage requires a confirming survey.
				if c.surveyed && !c.confirmed {
					reads, err := s.confirm(ctx, req, c)
					if err != nil {
						return Snapshot{}, err
					}
					if reads > 0 {
						moved = true
					}
				}
			case s.deltas.moved(root.Path, capture.plan):
				moved = true
				next := s.deltas.pending(root.Path, capture.plan)
				if next.overflow {
					if err := s.resurvey(ctx, req, c); err != nil {
						return Snapshot{}, err
					}
				} else if err := s.applyDelta(ctx, req, c, next.paths); err != nil {
					return Snapshot{}, err
				}
				capture.plan = next
			}
		}
		if !moved {
			return s.publishCandidates(ctx, req, captures, CaptureExact, started)
		}
		stalled++
		slog.DebugContext(ctx, "source observation pass",
			"roots_key", rootsKey(req.Roots), "pass", pass, "stalled", stalled)
		if stalled >= maxStalledPasses+1 {
			break
		}
	}
	// Stable reads with complete coverage are exact as of the captured plans.
	unstable := 0
	quality := CaptureExact
	for _, capture := range captures {
		c := capture.candidate
		unstable += len(c.unstable)
		if len(c.unstable) > 0 || !repochange.Coverage(c.root.Path).Complete {
			quality = CaptureObserved
		}
	}
	slog.InfoContext(ctx, "source snapshot published while the tree was still moving",
		"roots_key", rootsKey(req.Roots), "stalled_passes", stalled, "uncapturable", unstable, "quality", string(quality))
	return s.publishCandidates(ctx, req, captures, quality, started)
}

// candidate applies retained deltas when a covered generation is available.
func (s *Store) candidate(ctx context.Context, req Request, root Root, plan deltaPlan) (*rootCandidate, error) {
	if plan.covered == "" {
		return s.survey(ctx, req, root)
	}
	scope := s.scopeFor(ctx, req, root)
	if scope.Identity() != plan.scopeID {
		return s.survey(ctx, req, root)
	}
	head, err := s.Get(ctx, plan.covered)
	if err != nil {
		if !errors.Is(err, ErrSnapshotNotFound) {
			return nil, err
		}
		return s.survey(ctx, req, root)
	}
	baseline, err := s.newObserved(ctx, root.Path)
	if err != nil {
		return nil, err
	}
	c := &rootCandidate{
		root: root, scope: scope, baseline: baseline,
		ids:        &identifier{root: root.Path},
		boundaries: make(map[string]Boundary),
		base:       head.ID, overrides: make(map[byte]map[string]*Entry),
	}
	c.baseChunks, err = s.chunkMap(ctx, head.ID)
	if err != nil {
		c.close(ctx, s)
		return nil, err
	}
	for _, b := range head.Boundaries {
		if b.RootPath == root.Path {
			c.boundaries[b.Path] = b
		}
	}
	if err := s.applyDelta(ctx, req, c, plan.paths); err != nil {
		c.close(ctx, s)
		return nil, err
	}
	return c, nil
}

// survey reuses verified content identities for unchanged files.
func (s *Store) survey(ctx context.Context, req Request, root Root) (*rootCandidate, error) {
	if s.onSurvey != nil {
		s.onSurvey(root.Path)
	}
	baseline, err := s.newObserved(ctx, root.Path)
	if err != nil {
		return nil, err
	}
	c := &rootCandidate{
		root: root, scope: s.scopeFor(ctx, req, root), baseline: baseline,
		ids:        &identifier{root: root.Path, eager: true},
		boundaries: make(map[string]Boundary), surveyed: true,
		build: newBuildID(),
	}
	files := 0
	boundaries, err := admittedFiles(ctx, root, c.scope, func(ref fileRef) error {
		files++
		return s.captureInto(ctx, req, c, ref)
	})
	if err != nil {
		c.close(ctx, s)
		return nil, err
	}
	if err := errors.Join(c.flushStaging(ctx, s), c.baseline.flush(ctx, s)); err != nil {
		c.close(ctx, s)
		return nil, err
	}
	for _, b := range boundaries {
		c.boundaries[b.Path] = b
	}
	if err := s.forgetVanished(ctx, c); err != nil {
		c.close(ctx, s)
		return nil, err
	}
	slog.DebugContext(ctx, "source snapshot root surveyed",
		"root", root.Path, "files", files, "boundaries", len(boundaries),
		"verify", string(req.Verify), "reused", c.baseline.hits, "hashed", c.baseline.reads)
	return c, nil
}

func (s *Store) resurvey(ctx context.Context, req Request, c *rootCandidate) error {
	fresh, err := s.survey(ctx, req, c.root)
	if err != nil {
		return err
	}
	old := *c
	*c = *fresh
	old.close(ctx, s)
	return nil
}

// confirm reports new reads or a changed manifest after resurveying.
func (s *Store) confirm(ctx context.Context, req Request, c *rootCandidate) (int, error) {
	before, err := c.chunkIDs(ctx, s)
	if err != nil {
		return 0, err
	}
	if err := s.resurvey(ctx, req, c); err != nil {
		return 0, err
	}
	c.confirmed = true
	after, err := c.chunkIDs(ctx, s)
	if err != nil {
		return 0, err
	}
	if !maps.Equal(before, after) {
		return 1, nil
	}
	return c.baseline.reads, nil
}

// applyDelta surveys changed subtrees and removes vanished paths.
func (s *Store) applyDelta(ctx context.Context, req Request, c *rootCandidate, paths []string) error {
	for _, rel := range collapsePaths(paths) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rel == "." {
			return s.resurvey(ctx, req, c)
		}
		abs := absPath(c.root.Path, rel)
		info, err := os.Lstat(abs)
		switch {
		case err != nil && os.IsNotExist(err):
			if err := c.drop(ctx, s, rel); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.IsDir():
			if err := c.drop(ctx, s, rel); err != nil {
				return err
			}
			if !c.scope.AdmitPath(rel, true) {
				continue
			}
			boundaries, err := admittedUnder(ctx, c.root, c.scope, rel, func(ref fileRef) error {
				return s.captureInto(ctx, req, c, ref)
			})
			if err != nil {
				return err
			}
			for _, b := range boundaries {
				c.boundaries[b.Path] = b
			}
		default:
			if err := c.drop(ctx, s, rel); err != nil {
				return err
			}
			if !info.Mode().IsRegular() || !c.scope.AdmitPath(rel, false) {
				continue
			}
			if err := s.captureInto(ctx, req, c, fileRef{Path: rel, Abs: abs}); err != nil {
				return err
			}
		}
	}
	return errors.Join(c.flushStaging(ctx, s), c.baseline.flush(ctx, s))
}

func (s *Store) captureInto(ctx context.Context, req Request, c *rootCandidate, ref fileRef) error {
	release, err := s.broker.Acquire(ctx, backgroundwork.Request{
		Key: c.root.Path + ":snapshot", Lane: c.root.Path,
		Priority:  backgroundwork.PriorityIntegrity,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceIO},
	})
	if err != nil {
		return err
	}
	entry, err := s.identify(ctx, c.baseline, c.ids, ref, req.Verify)
	release()
	switch {
	case errors.Is(err, errVanished), errors.Is(err, errSkipped):
		return nil
	case errors.Is(err, errUnstable):
		c.unstable = append(c.unstable, ref.Path)
		return nil
	case err != nil:
		_ = c.baseline.flush(ctx, s)
		return err
	}
	if err := c.put(ctx, s, entry); err != nil {
		return err
	}
	if len(c.baseline.pending) >= observationFlushEvery {
		return c.baseline.flush(ctx, s)
	}
	return nil
}

func (s *Store) publishCandidates(ctx context.Context, req Request, captures []rootCapture, quality CaptureQuality, started time.Time) (Snapshot, error) {
	candidates := make([]*rootCandidate, 0, len(captures))
	boundaries := make([]Boundary, 0)
	// Retained budget boundaries keep the snapshot admission bounded.
	admission := AdmissionScope
	for _, capture := range captures {
		c := capture.candidate
		candidates = append(candidates, c)
		for _, b := range c.boundaries {
			boundaries = append(boundaries, b)
			if b.Reason == string(sandbox.BoundaryUnreadable) {
				quality = CaptureObserved
			}
			if b.Budgeted() {
				admission = AdmissionScopeBounded
			}
		}
	}
	snapshot, err := s.publish(ctx, req, candidates, boundaries, quality, admission)
	if err != nil {
		return Snapshot{}, err
	}
	for _, capture := range captures {
		c := capture.candidate
		exact := quality == CaptureExact && repochange.Coverage(c.root.Path).Complete
		s.deltas.coveredBy(c.root.Path, snapshot.ID, c.scope.Identity(), capture.plan.upTo, exact)
	}
	slog.InfoContext(ctx, "source snapshot published",
		"roots_key", snapshot.RootsKey, "snapshot_id", snapshot.ID, "files", snapshot.FileCount,
		"boundaries", len(boundaries), "unobserved", len(snapshot.Unobserved()),
		"quality", string(quality), "admission", string(admission), "duration_ms", time.Since(started).Milliseconds())
	return snapshot, nil
}

func (s *Store) scopeFor(ctx context.Context, req Request, root Root) *sourcescope.Scope {
	if req.Scope != nil {
		return req.Scope
	}
	s.mu.Lock()
	provider := s.scopes
	s.mu.Unlock()
	return provider.Capture(ctx, root.Path)
}

// Nested paths share their ancestor subtree observation.
func collapsePaths(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = cleanRelDir(p)
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		cleaned = append(cleaned, p)
	}
	sort.Strings(cleaned)
	out := make([]string, 0, len(cleaned))
	for _, p := range cleaned {
		if p == "." {
			return []string{"."}
		}
		if len(out) > 0 {
			last := out[len(out)-1]
			if strings.HasPrefix(p, last+"/") {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
