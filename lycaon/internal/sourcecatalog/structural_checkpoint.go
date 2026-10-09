package sourcecatalog

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const structuralFormat = "structural-tree-v3"
const structuralFileSuffix = ".tree"

// A checkpoint restores directory by directory, so one a few minutes old costs
// only the directories that changed since. Writing one is a whole-tree copy.
const structuralCheckpointInterval = 5 * time.Minute

// Checkpoint scheduling owns its admission and cancellation independently of publication.
type structuralCheckpoint struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	pending   bool
	drained   bool
	lastError error
	nextWrite time.Time
}

func (s *structuralCheckpoint) Schedule(parent context.Context, write func(context.Context) (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drained {
		return
	}
	s.pending = true
	if s.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, write)
}

func (s *structuralCheckpoint) run(ctx context.Context, write func(context.Context) (bool, error)) {
	for {
		s.mu.Lock()
		if ctx.Err() != nil || !s.pending {
			s.finishLocked()
			s.mu.Unlock()
			return
		}
		next := s.nextWrite
		s.mu.Unlock()
		timer := time.NewTimer(max(0, time.Until(next)))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.mu.Lock()
			s.finishLocked()
			s.mu.Unlock()
			return
		case <-timer.C:
		}
		s.mu.Lock()
		s.pending = false
		s.mu.Unlock()
		written, err := write(ctx)
		s.mu.Lock()
		s.lastError = err
		if written {
			s.nextWrite = time.Now().Add(structuralCheckpointInterval)
		}
		s.mu.Unlock()
	}
}

func (s *structuralCheckpoint) finishLocked() {
	s.cancel()
	close(s.done)
	s.done, s.cancel = nil, nil
}

// checkpointStructure writes the head when it is complete. An incomplete head
// could never restore, so it is skipped and the next complete publication
// schedules the write again.
func (s *indexStore) checkpointStructure(ctx context.Context) (bool, error) {
	release, err := s.stores.broker.Acquire(ctx, backgroundwork.Request{
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
	started := time.Now()
	if err := s.writePinnedCheckpoint(ctx, pin); err != nil {
		return false, err
	}
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

func (s *structuralCheckpoint) Drain() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drained = true
	s.pending = false
	if s.cancel != nil {
		s.cancel()
	}
	return s.done
}

func (s *structuralCheckpoint) Drained() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drained
}

func (s *structuralCheckpoint) Status() (<-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done, s.lastError
}

func (s *structuralCheckpoint) Active() bool {
	done, _ := s.Status()
	return done != nil
}
