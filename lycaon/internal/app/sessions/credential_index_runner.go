package sessions

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// kick clears a failed pass and schedules a stale root. It requires i.mu.
func (i *credentialIndex) kick(root *credentialRoot, starterID string) {
	if root.running || root.withdrawn || i.current(root) {
		return
	}
	root.err = nil
	i.schedule(root, starterID)
}

// schedule starts the root's runner unless one is active. It requires i.mu.
func (i *credentialIndex) schedule(root *credentialRoot, starterID string) {
	if root.running || root.withdrawn {
		return
	}
	ctx, finish, err := i.work.Begin(context.Background())
	if err != nil {
		root.err = err
		i.signal(root)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	root.running, root.cancel = true, cancel
	root.lastStats = credentialBuildStats{starterID: starterID}
	go func() {
		defer finish()
		defer cancel()
		i.run(ctx, root, starterID)
	}()
}

// signal wakes the root's waiters. It requires i.mu.
func (i *credentialIndex) signal(root *credentialRoot) {
	close(root.settled)
	root.settled = make(chan struct{})
}

func (i *credentialIndex) run(ctx context.Context, root *credentialRoot, starterID string) {
	for {
		i.mu.Lock()
		step, ok := i.next(root)
		if !ok {
			root.running, root.cancel = false, nil
			i.signal(root)
			i.mu.Unlock()
			return
		}
		root.applying = true
		i.mu.Unlock()
		stats, err := i.apply(ctx, root, step, starterID)
		i.mu.Lock()
		i.settle(root, step, stats, err)
		i.mu.Unlock()
	}
}

// next chooses the root's next step. It requires i.mu.
func (i *credentialIndex) next(root *credentialRoot) (credentialStep, bool) {
	for {
		if root.withdrawn || root.err != nil {
			return credentialStep{}, false
		}
		if !root.ready || root.full || root.coverageExpired(i.coverage(root.path), i.now()) {
			root.full = false
			epoch := repochange.CurrentEpoch(root.path)
			root.queue = slices.DeleteFunc(root.queue, func(event repochange.Event) bool {
				return eventEpoch(event).BootID == epoch.BootID && event.WorktreeEpoch <= epoch.Value
			})
			return credentialStep{epoch: epoch, full: true}, true
		}
		if len(root.queue) == 0 {
			if repochange.EpochCurrent(root.path, root.epoch) {
				return credentialStep{}, false
			}
			root.full = true
			continue
		}
		event := root.queue[0]
		root.queue = root.queue[1:]
		epoch := eventEpoch(event)
		if epoch.BootID == root.epoch.BootID && epoch.Value <= root.epoch.Value {
			continue
		}
		if !event.ContiguousAfter(root.epoch) {
			root.full = true
			continue
		}
		switch {
		case event.Kind == repochange.IndexChanged:
			return credentialStep{epoch: epoch}, true
		case event.Kind == repochange.HeadMoved && i.coverage(root.path).Complete:
			return credentialStep{epoch: epoch}, true
		case event.Kind == repochange.WorktreeChanged && len(event.Paths) > 0:
			return credentialStep{epoch: epoch, paths: event.Paths}, true
		}
		root.full = true
	}
}

// settle records a step's outcome. It requires i.mu.
func (i *credentialIndex) settle(root *credentialRoot, step credentialStep, stats credentialBuildStats, err error) {
	root.applying = false
	if stats.buildID != "" {
		root.lastStats = stats
	}
	if err != nil {
		root.err, root.ready = err, false
	} else {
		root.epoch = step.epoch
		if step.full {
			root.ready, root.validated = true, i.now()
			root.passes++
		}
	}
	i.signal(root)
}

func (i *credentialIndex) apply(ctx context.Context, root *credentialRoot, step credentialStep, starterID string) (credentialBuildStats, error) {
	if !step.full && len(step.paths) == 0 {
		return credentialBuildStats{}, nil
	}
	release, err := i.access.Acquire(ctx, root.key())
	if err != nil {
		return credentialBuildStats{}, err
	}
	defer release()
	root.directories.Store(0)
	root.credentialFiles.Store(0)
	if step.full {
		return i.refresh(ctx, root, step.epoch.Value, starterID, []string{"."})
	}
	return i.refresh(ctx, root, step.epoch.Value, starterID, step.paths)
}

func eventEpoch(event repochange.Event) repochange.Epoch {
	return repochange.Epoch{BootID: event.EpochBootID, Value: event.WorktreeEpoch}
}

// changed queues a watcher event for every admitted root it names.
func (i *credentialIndex) changed(event repochange.Event) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, root := range i.roots {
		if !sameCredentialRootDir(root.path, event.ProjectDir) || !root.ready && !root.running {
			continue
		}
		at, _ := slices.BinarySearchFunc(root.queue, event.WorktreeEpoch, func(queued repochange.Event, value uint64) int {
			return compareUint(queued.WorktreeEpoch, value)
		})
		root.queue = slices.Insert(root.queue, at, event)
		i.schedule(root, "")
	}
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// withdraw stops a root's work and retires its bindings in the background. It requires i.mu.
func (i *credentialIndex) withdraw(root *credentialRoot) {
	i.detach(root)
	ctx, finish, err := i.work.Begin(context.Background())
	if err != nil {
		return
	}
	go func() {
		defer finish()
		if err := i.retire(ctx, root); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "credential-file evidence retirement failed", "root", root.path, "error", err)
		}
	}()
}

// detach removes a root from the index and cancels its runner. It requires i.mu.
func (i *credentialIndex) detach(root *credentialRoot) {
	root.withdrawn = true
	if root.cancel != nil {
		root.cancel()
	}
	if i.roots[root.key()] == root {
		delete(i.roots, root.key())
	}
	i.signal(root)
}

func noteRootReadiness(
	ctx context.Context,
	localTrace *secretmatch.EvidenceTrace,
	waitMS int64,
	rootID string,
	generation uint64,
	waited bool,
	callerID string,
	stats credentialBuildStats,
) {
	buildID, dirs, classified, read := "", 0, 0, 0
	started := false
	if waited && stats.buildID != "" {
		buildID, dirs, classified, read = stats.buildID, stats.directoriesListed, stats.pathsClassified, stats.credentialFilesRead
		started = callerID != "" && stats.starterID == callerID
	} else {
		waitMS = 0
	}
	localTrace.NoteRootGenerationWait(waitMS, rootID, buildID, generation, started, dirs, classified, read)
	if trace := secretmatch.EvidenceTraceFrom(ctx); trace != nil {
		trace.NoteRootGenerationWait(waitMS, rootID, buildID, generation, started, dirs, classified, read)
	}
}

func emitReadinessPhase(ctx context.Context, projectID string, localTrace *secretmatch.EvidenceTrace, failed bool) {
	snap := localTrace.Snapshot()
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID:              secretmatch.ScreenCallIDFrom(ctx),
		ProjectID:           projectID,
		Phase:               "root_readiness_wait",
		DurationMS:          snap.ReadinessWaitMS,
		RootsChecked:        snap.RootsChecked,
		BuildsStarted:       snap.BuildsStarted,
		BuildsJoined:        snap.BuildsJoined,
		BuildIDs:            snap.BuildIDs,
		RootBuilds:          snap.RootBuilds,
		DirectoriesListed:   snap.DirectoriesListed,
		PathsClassified:     snap.PathsClassified,
		CredentialFilesRead: snap.CredentialFilesRead,
		Failed:              failed,
	})
}
