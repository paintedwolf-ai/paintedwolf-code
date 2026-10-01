package historyretention

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordingUsageCountsStoredBytesAndSharedContentOnce(t *testing.T) {
	s := newTestService(t)
	dir := filepath.Join(s.DataDir, "artifact-fixture")
	records := visual.NewRecords(s.Database, eventoutbox.New(s.Database, nil), visual.ArtifactProjection{})
	store := visual.NewDurableStore(visual.DurableConfig{
		DataDir:      s.DataDir,
		ArtifactsDir: func(string) (string, error) { return dir, os.MkdirAll(dir, 0o700) },
		Lookup:       func(context.Context, string) (string, error) { return testdbseed.DefaultProjectID, nil },
		Records:      records,
	})
	raw := append(visual.TestPNG1x1Bytes(), bytes.Repeat([]byte("retained exact frame bytes"), 4000)...)
	entry := visual.Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: raw, AutomaticRecording: true}
	wire, err := store.Put(t.Context(), "session", entry)
	testutil.FailErr(t, "publish compressed automatic media", err)
	rec, found, err := records.Get(t.Context(), wire.ID)
	testutil.FailErr(t, "read media sizes", err)
	if !found {
		t.Fatal("record absent")
	}
	info, err := os.Stat(filepath.Join(dir, rec.ContentHash))
	testutil.FailErr(t, "measure encoded object", err)
	if info.Size() != rec.StoredSize || rec.StoredSize >= int64(len(raw)) {
		t.Fatalf("incorrect encoded sizes: %+v", rec)
	}
	size, err := s.classBytes(t.Context(), "recordings", "")
	testutil.FailErr(t, "recording policy byte total", err)
	if size != info.Size() {
		t.Fatalf("policy counts %d want stored %d", size, info.Size())
	}
	checkLanes := func(recordings, artifacts int64) {
		t.Helper()
		testutil.FailErr(t, "measure compressed media lanes", s.refreshUsage(t.Context()))
		for _, lane := range s.lanes {
			want := recordings
			if lane.ID == "artifacts" {
				want = artifacts
			} else if lane.ID != "recordings" {
				continue
			}
			if lane.StoredBytes != want {
				t.Fatalf("lane %+v want stored %d", lane, want)
			}
			if want > 0 && lane.LogicalBytes != int64(len(raw)) {
				t.Fatalf("lane lost original media size: %+v", lane)
			}
		}
	}
	checkLanes(info.Size(), 0)
	_, err = store.Put(t.Context(), "session", entry)
	testutil.FailErr(t, "publish another owner of identical recording", err)
	checkLanes(info.Size(), 0)
	entry.AutomaticRecording = false
	_, err = store.Put(t.Context(), "session", entry)
	testutil.FailErr(t, "retain user artifact sharing media", err)
	checkLanes(0, info.Size())
	candidates, err := listCandidates(t.Context(), s.Database, "recordings", "", "2100-01-01T00:00:00Z", "", "")
	testutil.FailErr(t, "inspect shared recording protection", err)
	for _, candidate := range candidates {
		if candidate.ReclaimableBytes != 0 {
			t.Fatalf("shared user artifact counted reclaimable: %+v", candidate)
		}
	}
}
