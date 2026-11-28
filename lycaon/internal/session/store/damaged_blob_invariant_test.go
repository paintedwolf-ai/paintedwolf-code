package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestDamagedStoredBlobNeverDisablesSessionInvariant proves GB10 I6 (first case
// GB08-F1): corrupting one persisted content-addressed blob still lets the
// session load, the damaged item never grounds, one typed diagnostic is
// recorded, healthy items still ground, and verified re-observation repairs it.
func TestDamagedStoredBlobNeverDisablesSessionInvariant(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		damaged evidence.Record
		excerpt string
		line    int
	}{
		{
			name: "file_region_read",
			damaged: evidence.Record{
				Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion,
				Fidelity: evidence.FidelityStructured, SourceTool: "read",
				Path: "src/damaged.go", LineRanges: []evidence.LineRange{{Start: 10, End: 10}},
				Body: []string{"10\tfunc DamagedTarget() int { return 7 }"},
			},
			excerpt: "func DamagedTarget() int { return 7 }",
			line:    10,
		},
		{
			name: "command_output",
			damaged: evidence.Record{
				Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand,
				Fidelity: evidence.FidelityOpaque, SourceTool: "command",
				Body: []string{"status_code: 0", "PASS: damaged_cmd_check"},
			},
			excerpt: "PASS: damaged_cmd_check",
		},
		{
			name: "artifact_record",
			damaged: evidence.Record{
				Handle: "artifact#1", Kind: "artifact", Shape: evidence.ShapeArtifact,
				Fidelity: evidence.FidelityStructured, SourceTool: "create_artifact",
				Path: "artifacts/out.md", Body: []string{"artifact_payload_verified"},
			},
			excerpt: "artifact_payload_verified",
		},
		{
			name: "opaque_record",
			damaged: evidence.Record{
				Handle: "opaque#1", Kind: "opaque", Shape: evidence.ShapeOpaque,
				Fidelity: evidence.FidelityOpaque, SourceTool: "mcp__ext__probe",
				Body: []string{"opaque_payload_verified"},
			},
			excerpt: "opaque_payload_verified",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
			testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
			repos := NewSQL(sqlDB)
			repos.SetDataDir(dir)
			ctx := context.Background()

			sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "Create session", err)

			healthy := evidence.Record{
				Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion,
				Fidelity: evidence.FidelityStructured, SourceTool: "read",
				Path: "src/healthy.go", LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
				Body: []string{"1\tfunc HealthySibling() {}"},
			}
			testutil.FailErr(t, "Upsert damaged target", repos.Evidence.UpsertEvidenceRecord(ctx, sess.ID, tc.damaged))
			testutil.FailErr(t, "Upsert healthy sibling", repos.Evidence.UpsertEvidenceRecord(ctx, sess.ID, healthy))

			var sha string
			testutil.FailErr(t, "read target blob sha", sqlDB.QueryRowContext(ctx,
				`SELECT content_blob_sha256 FROM evidence_records WHERE session_id = ? AND handle = ?`,
				sess.ID, tc.damaged.Handle).Scan(&sha))
			rel, err := contentblob.RelPath(sha)
			testutil.FailErr(t, "RelPath", err)
			blobPath := filepath.Join(contentblob.StoreFor(dir, testdbseed.DefaultProjectID).Root, filepath.FromSlash(rel))

			// Corrupt the stored blob on disk with invalid zstd bytes.
			testutil.FailErr(t, "corrupt blob", os.WriteFile(blobPath, []byte("corrupted-non-zstd-payload"), 0o600))

			// 1. Session ledger must still load without error.
			ev, err := repos.Evidence.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "LoadLedger with one damaged blob", err)

			// 2. Damaged item must never ground.
			damagedRes := evidence.ResolveObservation(evidence.CitationRoots{}, ev, tc.damaged.Handle, evidence.Triple{
				Path:    tc.damaged.Path,
				Line:    tc.line,
				Excerpt: tc.excerpt,
			})
			if damagedRes.Verdict.Grounded() {
				t.Fatalf("damaged handle %s grounded as %q", tc.damaged.Handle, damagedRes.Verdict)
			}
			if evidence.ExcerptMatchesHandle(ev, tc.damaged.Handle, tc.damaged.Path, tc.line, tc.excerpt) {
				t.Fatalf("damaged handle %s matched excerpt", tc.damaged.Handle)
			}

			// 3. Healthy sibling still loads and grounds normally.
			healthyRes := evidence.ResolveObservation(evidence.CitationRoots{}, ev, healthy.Handle, evidence.Triple{
				Path:    healthy.Path,
				Line:    1,
				Excerpt: "func HealthySibling() {}",
			})
			if !healthyRes.Verdict.Grounded() {
				t.Fatalf("healthy sibling failed to ground: %+v", healthyRes)
			}

			// 4. Repeated loads emit one deduplicated diagnostic.
			_, err = repos.Evidence.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "second LoadLedger", err)
			if got := repos.core.lostBodies.Len(); got != 1 {
				t.Fatalf("typed lost-body diagnostics recorded = %d, want 1", got)
			}

			// 5. Verified re-observation repairs the damaged blob on disk.
			testutil.FailErr(t, "re-observe damaged record", repos.Evidence.UpsertEvidenceRecord(ctx, sess.ID, tc.damaged))
			repaired, err := repos.Evidence.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "LoadLedger after repair", err)
			if !evidence.ExcerptMatchesHandle(repaired, tc.damaged.Handle, tc.damaged.Path, tc.line, tc.excerpt) {
				t.Fatalf("re-observed handle %s did not repair damaged blob", tc.damaged.Handle)
			}
		})
	}
}
