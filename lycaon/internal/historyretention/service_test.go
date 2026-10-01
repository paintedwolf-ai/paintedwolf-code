package historyretention

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/upgradefixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	s := New(database, filepath.Join(root, "store.db"), nil)
	s.Now = func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(s.closePlans)
	return s
}

func seedReceipts(t *testing.T, s *Service, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := s.Database.ExecContext(t.Context(), `INSERT INTO llm_calls(id,project_id,session_id,status,started_at,completed_at,prompt_tokens,estimated_nano_usd) VALUES(?,?,?,'reported',?,?,7,100)`, fmt.Sprintf("receipt-%04d", i), testdbseed.DefaultProjectID, "session", "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z")
		testutil.FailErr(t, "seed receipt", err)
	}
}

func agedReceiptRequest() api.HistoryRetentionRequest {
	p := DefaultPolicy()
	p.ReceiptDetail = api.HistoryRetentionRule{Mode: "max_age", MaxAgeDays: 30}
	return api.HistoryRetentionRequest{Policy: p}
}

func TestDefaultPolicyRetainsAllHistoricalClasses(t *testing.T) {
	s := newTestService(t)
	seedReceipts(t, s, 1)
	p, err := s.Preview(t.Context(), api.HistoryRetentionRequest{Policy: DefaultPolicy()}, "")
	testutil.FailErr(t, "preview indefinite policy", err)
	if p.EligibleCount != 0 {
		t.Fatalf("indefinite policy selected %d owners", p.EligibleCount)
	}
	for _, class := range classes {
		_, err := listCandidates(t.Context(), s.Database, class, "", "2026-09-11T12:00:00Z", "", "")
		testutil.FailErr(t, "query "+class, err)
	}
	testutil.FailErr(t, "measure storage", s.refreshUsage(t.Context()))
}

func TestDefaultPolicyKeepsExactHistoricalBodiesAcrossDecades(t *testing.T) {
	s := newTestService(t)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, s.Database, testdbseed.DefaultProjectID, projectDir)
	evidence, err := upgradefixture.SeedSourceHistory(t.Context(), s.Database, s.DataDir, projectDir, testdbseed.DefaultProjectID, "session")
	testutil.FailErr(t, "seed durable historical bodies", err)
	seedReceipts(t, s, 1)
	for _, year := range []int{2050, 2100} {
		s.Now = func() time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }
		request := api.HistoryRetentionRequest{Policy: DefaultPolicy()}
		preview, err := s.Preview(t.Context(), request, "")
		testutil.FailErr(t, "evaluate decades of default retention", err)
		request.PreviewToken = preview.Token
		result, err := s.Prune(t.Context(), request, "")
		testutil.FailErr(t, "execute default no-loss selection", err)
		if preview.EligibleCount != 0 || result.RemovedCount != 0 {
			t.Fatalf("default policy removed history in %d: %+v", year, result)
		}
		testutil.FailErr(t, "reconstruct exact retained history decades later", upgradefixture.VerifySourceHistory(t.Context(), s.Database, s.DataDir, testdbseed.DefaultProjectID, "session", evidence))
		var receipts int
		testutil.FailErr(t, "count retained individual receipts", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM llm_calls`).Scan(&receipts))
		if receipts != 1 {
			t.Fatalf("default policy lost individual receipt detail in %d", year)
		}
	}
}

func TestRecoverySnapshotsReportLogicalBytesWithoutPhysicalAllocationClaim(t *testing.T) {
	s := newTestService(t)
	dir := filepath.Join(s.DataDir, "upgrade-recovery", "point", "projects", "project")
	testutil.FailErr(t, "create nested recovery fixture", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "seed retained recovery body", os.WriteFile(filepath.Join(dir, "body"), []byte("retained"), 0o600))
	testutil.FailErr(t, "measure recovery storage", s.refreshUsage(t.Context()))
	for _, lane := range s.lanes {
		if lane.ID == "upgrade-recovery" {
			if lane.LogicalBytes != 8 || lane.StoredBytes != 0 {
				t.Fatalf("recovery allocation was overstated: %+v", lane)
			}
			return
		}
	}
	t.Fatal("recovery storage lane missing")
}

func TestAttachmentUsageMeasuresCompressedFiles(t *testing.T) {
	s := newTestService(t)
	attachments := blobstore.Store{Root: project.HostDataDir(s.DataDir, testdbseed.DefaultProjectID), Dir: tooloutput.AttachmentSpillDir}
	body := strings.Repeat("repeated attachment content ", 4096)
	blob, err := attachments.Put("notes.txt", strings.NewReader(body), bytebound.Materialization(int64(len(body))))
	testutil.FailErr(t, "store compressed attachment", err)
	_, err = s.Database.ExecContext(t.Context(), `INSERT INTO prompt_attachment_blobs(project_id,blob_id,byte_size,created_at) VALUES(?,?,?,'2026-09-11T00:00:00Z')`, testdbseed.DefaultProjectID, blob.ID, len(body))
	testutil.FailErr(t, "retain attachment metadata", err)
	testutil.FailErr(t, "measure attachment storage", s.refreshUsage(t.Context()))
	for _, lane := range s.lanes {
		if lane.ID == "attachments" {
			if lane.LogicalBytes != int64(len(body)) || lane.StoredBytes <= 0 || lane.StoredBytes >= lane.LogicalBytes {
				t.Fatalf("attachment compression was not measured: %+v", lane)
			}
			return
		}
	}
	t.Fatal("attachment storage lane missing")
}

func TestReviewedReceiptPruningContinuesOnlyOriginalOwners(t *testing.T) {
	s := newTestService(t)
	seedReceipts(t, s, 300)
	request := agedReceiptRequest()
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview receipt policy", err)
	if preview.EligibleCount != 300 || len(preview.Candidates) != previewBatch || preview.Complete {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	request.PreviewToken = preview.Token
	var removed int64
	for {
		result, err := s.Prune(t.Context(), request, "")
		testutil.FailErr(t, "prune reviewed receipts", err)
		removed += result.RemovedCount
		if result.Complete {
			break
		}
		request.PreviewToken = result.PreviewToken
	}
	if removed != 300 {
		t.Fatalf("removed %d receipts, want300", removed)
	}
	var tombstones, tokens, cost int64
	testutil.FailErr(t, "count receipt tombstones", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM history_pruned_bodies WHERE class='receipt_detail'`).Scan(&tombstones))
	testutil.FailErr(t, "read retained spend", s.Database.QueryRowContext(t.Context(), `SELECT SUM(prompt_tokens),SUM(estimated_nano_usd) FROM llm_call_rollups`).Scan(&tokens, &cost))
	if tombstones != 300 || tokens != 2100 || cost != 30000 {
		t.Fatalf("lost receipt identity or totals: %d %d %d", tombstones, tokens, cost)
	}
}

func TestProtectionInvalidatesReviewAndExcludesOwner(t *testing.T) {
	s := newTestService(t)
	seedReceipts(t, s, 1)
	request := agedReceiptRequest()
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview", err)
	request.PreviewToken = preview.Token
	testutil.FailErr(t, "protect session", s.Protect(t.Context(), api.HistoryProtection{ScopeType: "session", ScopeID: "session", Protected: true}))
	if _, err := s.Prune(t.Context(), request, ""); !errors.Is(err, ErrPreviewChanged) {
		t.Fatalf("prune protected history: %v", err)
	}
	preview, err = s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview protected history", err)
	if preview.EligibleCount != 0 {
		t.Fatalf("protected owner selected: %+v", preview)
	}
}

func TestRestoredPolicyNeedsNewReview(t *testing.T) {
	s := newTestService(t)
	request := agedReceiptRequest()
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview", err)
	request.PreviewToken = preview.Token
	saved, err := s.SetPolicy(t.Context(), request, "")
	testutil.FailErr(t, "save reviewed policy", err)
	testutil.FailErr(t, "suspend restored policy", SuspendRestoredPolicy(s.DataDir))
	restored, err := readPolicy(s.DataDir)
	testutil.FailErr(t, "read restored policy", err)
	if !restored.Suspended || restored.Revision <= saved.Revision {
		t.Fatalf("restored policy remained enabled: %+v", restored)
	}
}

func TestActiveProjectIsExcludedWithoutHydratingHistory(t *testing.T) {
	s := newTestService(t)
	seedReceipts(t, s, 1)
	_, err := s.Database.ExecContext(t.Context(), `UPDATE sessions SET status='busy' WHERE id='session'`)
	testutil.FailErr(t, "mark busy", err)
	p, err := s.Preview(t.Context(), agedReceiptRequest(), "")
	testutil.FailErr(t, "preview busy project", err)
	if p.EligibleCount != 0 {
		t.Fatalf("busy project selected: %+v", p)
	}
}
