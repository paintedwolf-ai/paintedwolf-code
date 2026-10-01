package board

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPackContentHashStable(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	snap := fixtureSnapshot()
	snap.PackContentHash = ""
	h1 := PackContentHash(snap, now)
	h2 := PackContentHash(snap, now)
	if h1 == "" || h1 != h2 {
		t.Fatalf("h1=%q h2=%q", h1, h2)
	}
}

func TestPackContentHashWorkerFlip(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s1 := fixtureSnapshot()
	h1 := PackContentHash(s1, now)
	s2 := fixtureSnapshot()
	tasks := packboard.WorkerTasksFromSnapshot(s2)
	tasks[0].Status = api.WorkerStatusComplete
	(*s2.Workers)["tasks"] = tasks
	h2 := PackContentHash(s2, now)
	if h1 == h2 {
		t.Fatal("expected hash change when worker status flips")
	}
}

func TestPackContentHashDayKeyChange(t *testing.T) {
	snap := fixtureSnapshot()
	h1 := PackContentHash(snap, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	h2 := PackContentHash(snap, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	if h1 == h2 {
		t.Fatal("expected hash change across day boundary")
	}
}
