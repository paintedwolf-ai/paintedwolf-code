package contentblob_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// compressiblePayload distinguishes default and best compression levels.
func compressiblePayload(seed int64) []byte {
	words := strings.Fields(
		"the quick brown fox jumps over lazy dog while a coder writes tests for " +
			"content addressed blobs and density passes across many idle projects " +
			"with lots of repeated json envelope fields like content tool calls " +
			"reasoning finish reason provider model settled at created at")
	r := rand.New(rand.NewSource(seed))
	var sb strings.Builder
	for i := 0; i < 8000; i++ {
		sb.WriteString(words[r.Intn(len(words))])
		sb.WriteByte(' ')
	}
	return []byte(sb.String())
}

func TestRunDensityPassRecompressesShrinksAndFlipsColdRoundTrip(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertProject(t, database, projectID)
	ctx := t.Context()

	old := time.Now().UTC().Add(-48 * time.Hour)
	_, err := database.ExecContext(ctx, `UPDATE projects SET last_opened_at = ? WHERE id = ?`, db.FormatTime(old), projectID)
	testutil.FailErr(t, "age project", err)

	store := contentblob.StoreFor(dataDir, projectID)
	plaintext := compressiblePayload(42)
	sha, byteSize, storedSize, err := contentblob.Write(store, plaintext)
	testutil.FailErr(t, "write blob", err)

	q := db.New(database)
	testutil.FailErr(t, "upsert content blob object", q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}))

	var tierBefore string
	testutil.FailErr(t, "read tier before", database.QueryRowContext(ctx,
		`SELECT tier FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&tierBefore))
	if tierBefore != "hot" {
		t.Fatalf("tier before density pass = %q, want hot", tierBefore)
	}

	cfg := contentblob.DensityConfig{}
	cfg.Density.IdleThresholdDays = 1
	testutil.FailErr(t, "run density pass", contentblob.RunDensityPass(ctx, contentblob.DensityDeps{
		Queries: q, DataDir: dataDir,
	}, cfg))

	var tierAfter string
	var storedSizeAfter int64
	testutil.FailErr(t, "read tier after", database.QueryRowContext(ctx,
		`SELECT tier, stored_size FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).
		Scan(&tierAfter, &storedSizeAfter))
	if tierAfter != "cold" {
		t.Fatalf("tier after density pass = %q, want cold", tierAfter)
	}
	if storedSizeAfter >= storedSize {
		t.Fatalf("recompressed stored_size = %d, want smaller than original %d", storedSizeAfter, storedSize)
	}

	// Recompression preserves plaintext.
	got, err := contentblob.Read(store, sha)
	testutil.FailErr(t, "read cold blob", err)
	if string(got) != string(plaintext) {
		t.Fatalf("recompressed blob round-trip mismatch: got %d bytes, want %d bytes", len(got), len(plaintext))
	}

	var projectTier string
	testutil.FailErr(t, "read project storage tier", database.QueryRowContext(ctx,
		`SELECT storage_tier FROM projects WHERE id = ?`, projectID).Scan(&projectTier))
	if projectTier != "cold" {
		t.Fatalf("project storage_tier = %q, want cold once no hot rows remain", projectTier)
	}

	// The schema trigger makes reopened projects hot.
	_, err = database.ExecContext(ctx, `UPDATE projects SET last_opened_at = ? WHERE id = ?`,
		db.FormatTime(time.Now().UTC()), projectID)
	testutil.FailErr(t, "reopen project", err)
	testutil.FailErr(t, "read project storage tier after reopen", database.QueryRowContext(ctx,
		`SELECT storage_tier FROM projects WHERE id = ?`, projectID).Scan(&projectTier))
	if projectTier != "hot" {
		t.Fatalf("project storage_tier after reopen = %q, want hot", projectTier)
	}
}

func TestRunDensityPassSkipsProjectsNotIdle(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertProject(t, database, projectID) // last_opened_at defaults to now
	ctx := t.Context()

	store := contentblob.StoreFor(dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(store, compressiblePayload(7))
	testutil.FailErr(t, "write blob", err)
	q := db.New(database)
	testutil.FailErr(t, "upsert content blob object", q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}))

	cfg := contentblob.DensityConfig{}
	cfg.Density.IdleThresholdDays = 14
	testutil.FailErr(t, "run density pass", contentblob.RunDensityPass(ctx, contentblob.DensityDeps{
		Queries: q, DataDir: dataDir,
	}, cfg))

	var tier string
	testutil.FailErr(t, "read tier", database.QueryRowContext(ctx,
		`SELECT tier FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&tier))
	if tier != "hot" {
		t.Fatalf("a recently opened project must not be densified, tier = %q", tier)
	}
}
