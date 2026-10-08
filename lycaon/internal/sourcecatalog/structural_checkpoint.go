package sourcecatalog

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pagedview"
)

const structuralFormat = "structural-tree-v3"
const structuralFileSuffix = ".tree"

// A checkpoint restores directory by directory, so one a few minutes old costs
// only the directories that changed since. Writing one is a whole-tree copy.
const structuralCheckpointInterval = 5 * time.Minute

// The store mutex protects checkpoint scheduling, including drain admission.
type structuralCheckpoint struct {
	cancel         context.CancelFunc
	done           chan struct{}
	pending        bool
	drained        bool
	lastError      error
	nextWrite      time.Time
	fingerprint    pagedview.Fingerprint
	hasFingerprint bool
}

func (s *indexStore) scheduleStructuralCheckpoint(parent context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoint.drained || s.navigation.retired || s.pins.drained {
		return
	}
	s.checkpoint.pending = true
	if s.checkpoint.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	s.checkpoint.cancel = cancel
	s.checkpoint.done = make(chan struct{})
	go s.runStructuralCheckpoints(ctx)
}

func (s *indexStore) runStructuralCheckpoints(ctx context.Context) {
	for {
		s.mu.Lock()
		if ctx.Err() != nil || !s.checkpoint.pending {
			s.finishStructuralCheckpointLocked()
			s.mu.Unlock()
			return
		}
		next := s.checkpoint.nextWrite
		s.mu.Unlock()
		timer := time.NewTimer(max(0, time.Until(next)))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.mu.Lock()
			s.finishStructuralCheckpointLocked()
			s.mu.Unlock()
			return
		case <-timer.C:
		}
		s.mu.Lock()
		s.checkpoint.pending = false
		s.mu.Unlock()
		written, err := s.checkpointStructure(ctx)
		s.mu.Lock()
		s.checkpoint.lastError = err
		if written {
			s.checkpoint.nextWrite = time.Now().Add(structuralCheckpointInterval)
		}
		s.mu.Unlock()
	}
}

func (s *indexStore) finishStructuralCheckpointLocked() {
	s.checkpoint.cancel()
	close(s.checkpoint.done)
	s.checkpoint.done, s.checkpoint.cancel = nil, nil
}

// checkpointStructure writes the head when it is complete. An incomplete head
// could never restore, so it is skipped and the next complete publication
// schedules the write again.
func (s *indexStore) checkpointStructure(ctx context.Context) (bool, error) {
	release, err := s.catalog.broker.Acquire(ctx, backgroundwork.Request{
		Key: s.workKey() + ":checkpoint", Lane: s.root.Path,
		Priority: backgroundwork.PriorityProactive, Resources: []backgroundwork.Resource{backgroundwork.ResourceIO},
	})
	if err != nil {
		return false, err
	}
	defer release()
	pin, err := s.retainGeneration(0, true)
	if err != nil {
		return false, err
	}
	defer func() { pin.Release() }()
	if !pin.value.complete {
		return false, nil
	}

	replacement, err := s.compactCheckpointPin(ctx, pin)
	if err != nil {
		return false, err
	}
	if replacement != nil {
		pin.Release()
		pin = replacement
	}
	fingerprint, err := structuralCheckpointFingerprint(ctx, pin.value)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	unchanged := s.checkpoint.hasFingerprint && s.checkpoint.fingerprint == fingerprint
	s.mu.Unlock()
	if unchanged {
		return false, nil
	}

	started := time.Now()
	if err := s.writePinnedCheckpoint(ctx, pin); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.checkpoint.fingerprint, s.checkpoint.hasFingerprint = fingerprint, true
	s.mu.Unlock()
	slog.DebugContext(ctx, "Structural checkpoint written", "root", s.root.ID, "generation", pin.Generation,
		"directories", pin.value.directories.Len(), "write_ms", time.Since(started).Milliseconds())
	return true, nil
}

func (s *indexStore) writePinnedCheckpoint(ctx context.Context, pin *GenerationPin) error {
	reader, writer := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		err := writeStructuralCheckpoint(ctx, writer, s.root, pin.value)
		_ = writer.CloseWithError(err)
		finished <- err
	}()
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: filepath.Dir(s.structureFile), Rel: filepath.Base(s.structureFile)},
		Source:   reader, Mode: 0o600, DirMode: 0o700,
		BeforeCommit: func(fseffect.Target, fseffect.Result) error { return ctx.Err() },
	})
	_ = reader.CloseWithError(err)
	writeErr := <-finished
	if err != nil {
		return err
	}
	return writeErr
}

func (s *indexStore) drainStructuralCheckpointLocked() <-chan struct{} {
	s.checkpoint.drained = true
	s.checkpoint.pending = false
	if s.checkpoint.cancel != nil {
		s.checkpoint.cancel()
	}
	return s.checkpoint.done
}

// The recursive range fingerprint excludes generation and observation clocks.
func structuralCheckpointFingerprint(ctx context.Context, generation *structuralGeneration) (pagedview.Fingerprint, error) {
	root, _, err := generation.directories.Get(ctx, ".")
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	index := pagedview.RangeIndex[TreeItem]{Store: generation, Root: root.page}
	return DirectoryBodyFingerprint(ctx, &index, ".", stateOf(root.observation), true)
}
