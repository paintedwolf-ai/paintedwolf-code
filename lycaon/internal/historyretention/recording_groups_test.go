package historyretention

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func recordingGroupFixture(t *testing.T, projection visual.ArtifactProjection) (*Service, *visual.Records, func(string, bool) string, string) {
	t.Helper()
	s := newTestService(t)
	s.Now = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }
	testdbseed.InsertSession(t, s.Database, "other-session", testdbseed.DefaultProjectID)
	dir := filepath.Join(s.DataDir, "artifacts")
	records := visual.NewRecords(s.Database, eventoutbox.New(s.Database, nil), projection)
	s.Artifacts = visual.NewDurableStore(visual.DurableConfig{DataDir: s.DataDir, Records: records,
		Lookup:       func(context.Context, string) (string, error) { return testdbseed.DefaultProjectID, nil },
		ArtifactsDir: func(string) (string, error) { return dir, os.MkdirAll(dir, 0o700) },
	})
	put := func(session string, recording bool) string {
		t.Helper()
		wire, err := s.Artifacts.Put(t.Context(), session, visual.Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: visual.TestPNG1x1Bytes(), AutomaticRecording: recording})
		testutil.FailErr(t, "publish shared media", err)
		return wire.ID
	}
	return s, records, put, dir
}

func recordingPruneRequest() api.HistoryRetentionRequest {
	policy := DefaultPolicy()
	policy.Recordings = api.HistoryRetentionRule{Mode: "max_age", MaxAgeDays: 30}
	return api.HistoryRetentionRequest{Policy: policy}
}

func TestRecordingGroupPrunesAllEligibleOwnersAtomically(t *testing.T) {
	s, records, put, dir := recordingGroupFixture(t, visual.ArtifactProjection{})
	ids := []string{put("session", true), put("other-session", true)}
	rec, _, err := records.Get(t.Context(), ids[0])
	testutil.FailErr(t, "read shared size", err)
	request := recordingPruneRequest()
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview shared recordings", err)
	if preview.EligibleCount != 2 || preview.ReclaimableBytes != rec.StoredSize {
		t.Fatalf("shared preview: %+v", preview)
	}
	request.PreviewToken = preview.Token
	release := bloblifecycle.AcquirePublication(s.DataDir)
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	result, err := s.Prune(t.Context(), request, "")
	testutil.FailErr(t, "prune shared recording group", err)
	if result.RemovedCount != 2 || result.ReleasedBytes != rec.StoredSize {
		t.Fatalf("prune result: %+v", result)
	}
	for _, id := range ids {
		rec, _, err := records.Get(t.Context(), id)
		testutil.FailErr(t, "read tombstone", err)
		if !rec.Deleted() {
			t.Fatal("group left live owner")
		}
	}
	testutil.FailErr(t, "measure deferred cleanup", s.refreshUsage(t.Context()))
	var pending int64
	for _, lane := range s.lanes {
		if lane.ID == "artifact_cleanup" {
			pending = lane.StoredBytes
		}
	}
	if pending != rec.StoredSize {
		t.Fatalf("pending unlink bytes lost: %d want %d", pending, rec.StoredSize)
	}
	if _, err := os.Stat(filepath.Join(dir, rec.ContentHash)); err != nil {
		t.Fatalf("capture lost pending body: %v", err)
	}
	release()
	released = true
	testutil.FailErr(t, "finish deferred cleanup", s.Artifacts.CollectGarbage(t.Context()))
	if _, err := os.Stat(filepath.Join(dir, rec.ContentHash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan body remains: %v", err)
	}
}

func TestRecordingGroupsProtectEveryOutsideClaim(t *testing.T) {
	for _, kind := range []string{"user artifact", "cover", "external session", "protected session", "group overflow", "newer owner"} {
		t.Run(kind, func(t *testing.T) {
			s, _, put, _ := recordingGroupFixture(t, visual.ArtifactProjection{})
			id := put("session", true)
			second := put("other-session", true)
			switch kind {
			case "user artifact":
				put("session", false)
			case "cover", "external session":
				refKind, session := "project_cover", "session"
				if kind == "external session" {
					refKind = "message_attachment"
					session = "other-session"
				}
				_, err := s.Database.ExecContext(t.Context(), `INSERT INTO artifact_refs(id,artifact_id,project_id,kind,session_id,created_at) VALUES('pin',?,?,?,?,?)`, id, testdbseed.DefaultProjectID, refKind, session, "2020-01-01T00:00:00Z")
				testutil.FailErr(t, "pin media reference", err)
			case "protected session":
				testutil.FailErr(t, "protect group member", s.Protect(t.Context(), api.HistoryProtection{ScopeType: "session", ScopeID: "other-session", Protected: true}))
			case "group overflow":
				for range maxGroupOwners - 1 {
					put("session", true)
				}
			case "newer owner":
				_, err := s.Database.ExecContext(t.Context(), `UPDATE artifacts SET created_at='2100-01-01T00:00:00Z' WHERE id=?`, second)
				testutil.FailErr(t, "retain younger owner", err)
			}
			preview, err := s.Preview(t.Context(), recordingPruneRequest(), "")
			testutil.FailErr(t, "preview protected group", err)
			if preview.EligibleCount != 0 {
				t.Fatalf("selected protected group: %+v", preview)
			}
		})
	}
}

func TestRecordingGroupProjectionFailureRollsBackAllOwners(t *testing.T) {
	calls := 0
	projection := visual.ArtifactProjection{Delete: func(ctx context.Context, tx *sql.Tx, id string) error {
		calls++
		if calls == 2 {
			return errors.New("projection failed")
		}
		_, err := tx.ExecContext(ctx, `UPDATE artifacts SET caption='projection changed' WHERE id=?`, id)
		return err
	}}
	s, records, put, dir := recordingGroupFixture(t, projection)
	ids := []string{put("session", true), put("other-session", true)}
	request := recordingPruneRequest()
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview group", err)
	request.PreviewToken = preview.Token
	if _, err := s.Prune(t.Context(), request, ""); err == nil {
		t.Fatal("injected projection failure accepted")
	}
	for _, id := range ids {
		rec, _, err := records.Get(t.Context(), id)
		testutil.FailErr(t, "read rolled back owner", err)
		if rec.Deleted() || rec.Caption != "" {
			t.Fatal("partial recording group tombstone/projection committed")
		}
		if _, err := os.Stat(filepath.Join(dir, rec.ContentHash)); err != nil {
			t.Fatalf("retained body disappeared: %v", err)
		}
	}
	var events, queued int
	testutil.FailErr(t, "count unchanged events", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&events))
	testutil.FailErr(t, "count rolled back cleanup", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM artifact_gc_queue`).Scan(&queued))
	if events != 2 || queued != 0 {
		t.Fatalf("partial group effects: events=%d cleanup=%d", events, queued)
	}

}
