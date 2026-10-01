package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0x0f, 0x00, 0x00,
	0x01, 0x01, 0x00, 0x05, 0x18, 0xd8, 0x4d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func TestCommitVisualEvidenceBindsHandleToArtifact(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		source  api.VisualArtifactSource
		args    map[string]any
		content string
	}{
		{
			name:    "fetch_url image",
			tool:    "fetch_url",
			source:  api.VisualArtifactSourceFetch,
			args:    map[string]any{"url": "https://example.org/chart.png"},
			content: `{"url":"https://example.org/chart.png","content_type":"image/png"}`,
		},
		{
			name:    "capture_page still",
			tool:    "capture_page",
			source:  api.VisualArtifactSourceCapture,
			args:    map[string]any{"url": "http://127.0.0.1:5173/"},
			content: `{"state":{},"snapshot":{"text":"ready"},"log":[],"mime":"image/png","width":1,"height":1,"final_url":"http://127.0.0.1:5173/"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			sqlDB := testdbfixture.Open(t, "evidence_visual.db")
			projectID := testdbseed.DefaultProjectID
			testdbseed.InsertProjectRoot(t, sqlDB, projectID, t.TempDir())
			sessions := NewSQL(sqlDB)
			sess, err := sessions.Create(ctx, api.CreateSessionRequest{ProjectID: projectID, Posture: api.SessionPostureBuild}, projectID)
			testutil.FailErr(t, "create session", err)

			records := visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{})
			sessions.SetArtifactRecords(records)
			dataDir := t.TempDir()
			artifacts := visual.NewDurableStore(visual.DurableConfig{
				DataDir: dataDir,
				ArtifactsDir: func(id string) (string, error) {
					dir := filepath.Join(dataDir, "projects", id, "artifacts")
					return dir, os.MkdirAll(dir, 0o700)
				},
				Lookup:  func(context.Context, string) (string, error) { return projectID, nil },
				Records: records,
			})
			stored, err := artifacts.Put(ctx, sess.ID, visual.Entry{
				Meta:  api.VisualArtifact{Mime: "image/png", Source: tc.source, Perceive: true, ToolCallID: "call-1"},
				Bytes: tinyPNG,
			})
			testutil.FailErr(t, "put artifact", err)

			handle, _, err := sessions.CommitVisualEvidenceToolResult(ctx, sess.ID, t.TempDir(), tc.tool, tc.args, tc.content, stored.ID)
			testutil.FailErr(t, "commit visual evidence", err)
			if handle == "" {
				t.Fatalf("%s minted no evidence handle", tc.tool)
			}

			id, res := visual.ResolveRef(ctx, artifacts, sess.ID, handle)
			if !res.IsPresent() || id != stored.ID {
				t.Fatalf("ResolveRef(%s) = %q present=%v reason=%v, want %s", handle, id, res.IsPresent(), res.Reason(), stored.ID)
			}
			if got := res.Meta().EvidenceHandle; got != handle {
				t.Fatalf("artifact evidence_handle = %q, want %q", got, handle)
			}
			ledger, err := sessions.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "load ledger", err)
			if rec := ledger.Handles[handle]; rec.ArtifactID != stored.ID {
				t.Fatalf("ledger %s artifact_id = %q, want %s", handle, rec.ArtifactID, stored.ID)
			}
		})
	}
}

func TestCommitVisualEvidenceRollsBackWhenArtifactMissing(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "evidence_visual_missing.db")
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertProjectRoot(t, sqlDB, projectID, t.TempDir())
	sessions := NewSQL(sqlDB)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{ProjectID: projectID, Posture: api.SessionPostureBuild}, projectID)
	testutil.FailErr(t, "create session", err)
	sessions.SetArtifactRecords(visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{}))

	_, _, err = sessions.CommitVisualEvidenceToolResult(ctx, sess.ID, t.TempDir(), "fetch_url",
		map[string]any{"url": "https://example.org/a.png"}, `{"url":"https://example.org/a.png"}`, "00000000-0000-4000-8000-000000000000")
	if err == nil {
		t.Fatal("binding to an absent artifact succeeded")
	}
	ledger, err := sessions.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "load ledger", err)
	if len(ledger.Handles) != 0 {
		t.Fatalf("ledger kept %d handles after a failed binding", len(ledger.Handles))
	}
}
