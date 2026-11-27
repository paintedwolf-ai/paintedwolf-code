package sourcefeed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type cf05CaptureSink struct {
	mu     sync.Mutex
	events []api.SourceChangesEvent
	err    error
	wakes  int
}

func (s *cf05CaptureSink) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, ev)
	return nil
}

func (s *cf05CaptureSink) SourceChangedTx(_ context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, ev)
	return nil
}

func (s *cf05CaptureSink) Deliver() {
	s.mu.Lock()
	s.wakes++
	s.mu.Unlock()
}

func (s *cf05CaptureSink) take() []api.SourceChangesEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]api.SourceChangesEvent(nil), s.events...)
	s.events = nil
	return out
}

func cf05SeedRNG(baseSeed, family string, idx int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", baseSeed, family, idx)))
	s1 := binary.BigEndian.Uint64(sum[0:8])
	s2 := binary.BigEndian.Uint64(sum[8:16])
	return rand.New(rand.NewPCG(s1, s2))
}

// TestCF05_F01_ChangeDifferentialInProcess runs 2,000 deterministically seeded F01 instances
// (seed 20261127) verifying every logged write is reported, nothing unlogged is reported,
// each observation carries one batch/workspace identity, and batches >512 emit resync=true.
func TestCF05_F01_ChangeDifferentialInProcess(t *testing.T) {
	sink := &cf05CaptureSink{}
	t.Cleanup(Bind(sink))

	ops := []api.SourceChangeOp{
		api.SourceChangeOpCreate,
		api.SourceChangeOpWrite,
		api.SourceChangeOpRename,
		api.SourceChangeOpDelete,
	}

	const instances = 2000
	for i := 0; i < instances; i++ {
		rng := cf05SeedRNG("20261127", "F01", i)
		writeCount := 1 + rng.IntN(200)
		if i == 0 {
			writeCount = 1
		} else if i == 1 {
			writeCount = 200
		} else if i == instances-1 {
			// Overflow boundary: > maxChangesPerEvent (512) must set Resync=true
			writeCount = maxChangesPerEvent + 17
		}

		projectID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		workspaceID := fmt.Sprintf("ws-f01-%04d", i)
		loggedPaths := make(map[string]struct{}, writeCount)
		changes := make([]Change, 0, writeCount)

		for j := 0; j < writeCount; j++ {
			op := ops[rng.IntN(len(ops))]
			path := fmt.Sprintf("dir_%02d/file_%04d.txt", j%16, j)
			ch := Change{
				ProjectID:     projectID,
				WorkspaceID:   workspaceID,
				WorkspaceKind: api.SourceWorkspaceKindProject,
				RootID:        "r1",
				Path:          path,
				Op:            op,
				Origin:        api.SourceChangeOriginExternal,
			}
			loggedPaths[path] = struct{}{}
			if op == api.SourceChangeOpRename {
				from := fmt.Sprintf("dir_%02d/old_%04d.txt", j%16, j)
				ch.FromPath = from
				loggedPaths[from] = struct{}{}
			}
			changes = append(changes, ch)
		}

		if err := EmitBatch(context.Background(), changes); err != nil {
			t.Fatalf("instance %d EmitBatch failed: %v", i, err)
		}
		got := sink.take()
		if len(got) != 1 {
			t.Fatalf("instance %d emitted %d batch events, want 1", i, len(got))
		}
		ev := got[0]
		if ev.ProjectID != projectID || ev.WorkspaceID != workspaceID {
			t.Fatalf("instance %d wrong batch identity (%q,%q)", i, ev.ProjectID, ev.WorkspaceID)
		}

		if writeCount > maxChangesPerEvent {
			if !ev.Resync {
				t.Fatalf("instance %d writeCount=%d expected Resync=true", i, writeCount)
			}
			if len(ev.Changes) != maxChangesPerEvent {
				t.Fatalf("instance %d capped changes=%d want %d", i, len(ev.Changes), maxChangesPerEvent)
			}
			continue
		}

		if ev.Resync {
			t.Fatalf("instance %d writeCount=%d unexpected Resync=true", i, writeCount)
		}
		observedPaths := make(map[string]struct{}, len(ev.Changes))
		for _, c := range ev.Changes {
			if _, ok := loggedPaths[c.Path]; !ok {
				t.Fatalf("instance %d reported unlogged path %q", i, c.Path)
			}
			observedPaths[c.Path] = struct{}{}
			if c.FromPath != "" {
				if _, ok := loggedPaths[c.FromPath]; !ok {
					t.Fatalf("instance %d reported unlogged from_path %q", i, c.FromPath)
				}
				observedPaths[c.FromPath] = struct{}{}
			}
		}
		for p := range loggedPaths {
			if _, ok := observedPaths[p]; !ok {
				t.Fatalf("instance %d missed logged path %q", i, p)
			}
		}
	}
}
