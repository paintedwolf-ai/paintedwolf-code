package project

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMemoryRegistrySetCover_latestWins(t *testing.T) {
	reg := NewMemoryRegistry()
	p, err := reg.Create(context.Background(), CreateParams{Name: "cover-test"})
	testutil.FailErr(t, "reg.Create failed", err)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	got, err := reg.SetCover(context.Background(), p.ID, "art-1", "sess-root", "capture", now)
	testutil.FailErr(t, "reg.SetCover failed", err)
	if got.CoverArtifactID != "art-1" || got.CoverRootSessionID != "sess-root" || got.CoverSource != "capture" {
		t.Fatalf("cover = %+v", got)
	}
	later := now.Add(time.Minute)
	got, err = reg.SetCover(context.Background(), p.ID, "art-2", "sess-root", "render", later)
	testutil.FailErr(t, "reg.SetCover failed", err)
	if got.CoverArtifactID != "art-2" || got.CoverSource != "render" {
		t.Fatalf("cover = %+v", got)
	}
	wire := ToAPI(got)
	if wire.CoverArtifactID == nil || *wire.CoverArtifactID != "art-2" {
		t.Fatalf("wire cover = %+v", wire.CoverArtifactID)
	}
	if wire.CoverRootSessionID == nil || *wire.CoverRootSessionID != "sess-root" {
		t.Fatalf("wire root = %+v", wire.CoverRootSessionID)
	}
}
