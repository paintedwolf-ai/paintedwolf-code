package historyretention

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLargeCheckpointGroupStillReleasesExclusiveBodies(t *testing.T) {
	s := newTestService(t)
	common, unique := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, sha := range []string{common, unique} {
		_, err := s.Database.ExecContext(t.Context(), `INSERT INTO source_blob_objects(sha256,size,stored_size,storage_relpath,git_oid_sha1,git_oid_sha256) VALUES(?,100,10,?,'','')`, sha, sha+".zst")
		testutil.FailErr(t, "seed checkpoint content", err)
	}
	for i := 0; i <= maxGroupOwners; i++ {
		anchor := fmt.Sprintf("anchor-%03d", i)
		_, err := s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_anchors(session_id,anchor_id,project_id,root_key,sealed_at,manifest_json) VALUES('session',?,?,'root','2020-01-01T00:00:00Z','{}')`, anchor, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "seed connected anchor", err)
		_, err = s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_object_refs(session_id,anchor_id,path,sha256,original_size,mode) VALUES('session',?,'common.go',?,100,420)`, anchor, common)
		testutil.FailErr(t, "reference common file", err)
	}
	_, err := s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_object_refs(session_id,anchor_id,path,sha256,original_size,mode) VALUES('session','anchor-000','unique.go',?,100,420)`, unique)
	testutil.FailErr(t, "reference exclusive file", err)
	policy := DefaultPolicy()
	policy.Checkpoints = api.HistoryRetentionRule{Mode: "max_bytes", MaxBytes: 10}
	request := api.HistoryRetentionRequest{Policy: policy}
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "review exclusive bytes in large connected history", err)
	if preview.EligibleCount != 1 || preview.ReclaimableBytes != 10 || preview.SharedProtectedBytes != 10 {
		t.Fatalf("common file blocked exclusive history pruning: %+v", preview)
	}
	request.PreviewToken = preview.Token
	result, err := s.Prune(t.Context(), request, "")
	testutil.FailErr(t, "release exclusive history", err)
	if result.RemovedCount != 1 || result.ReleasedBytes != 10 {
		t.Fatalf("unexpected exclusive release: %+v", result)
	}
	var commonRefs, uniqueRefs int
	testutil.FailErr(t, "count surviving common references", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM checkpoint_object_refs WHERE sha256=?`, common).Scan(&commonRefs))
	testutil.FailErr(t, "count released unique references", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM checkpoint_object_refs WHERE sha256=?`, unique).Scan(&uniqueRefs))
	if commonRefs != maxGroupOwners || uniqueRefs != 0 {
		t.Fatalf("unexpected remaining references: common=%d unique=%d", commonRefs, uniqueRefs)
	}
}

func TestSharedCheckpointGroupPrunesOnlyWhenEveryRequiredOwnerIsEligible(t *testing.T) {
	s := newTestService(t)
	testdbseed.InsertSession(t, s.Database, "second", testdbseed.DefaultProjectID)
	sha := strings.Repeat("a", 64)
	_, err := s.Database.ExecContext(t.Context(), `INSERT INTO source_blob_objects(sha256,size,stored_size,storage_relpath,git_oid_sha1,git_oid_sha256) VALUES(?,100,10,'aa/body.zst','','')`, sha)
	testutil.FailErr(t, "seed shared object", err)
	for _, id := range []string{"session", "second"} {
		_, err := s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_anchors(session_id,anchor_id,project_id,root_key,sealed_at,manifest_json) VALUES(?,'anchor',?,'root','2020-01-01T00:00:00Z','{}')`, id, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "seed anchor", err)
		_, err = s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_object_refs(session_id,anchor_id,path,sha256,original_size,mode) VALUES(?,'anchor','file.go',?,100,420)`, id, sha)
		testutil.FailErr(t, "seed checkpoint reference", err)
	}
	policy := DefaultPolicy()
	policy.Checkpoints = api.HistoryRetentionRule{Mode: "max_bytes", MaxBytes: 1}
	request := api.HistoryRetentionRequest{Policy: policy}
	testutil.FailErr(t, "protect shared owner", s.Protect(t.Context(), api.HistoryProtection{ScopeType: "session", ScopeID: "second", Protected: true}))
	preview, err := s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview protected shared content", err)
	if preview.EligibleCount != 0 || preview.SharedProtectedBytes != 10 {
		t.Fatalf("protected shared content selected: %+v", preview)
	}
	testutil.FailErr(t, "release explicit protection", s.Protect(t.Context(), api.HistoryProtection{ScopeType: "session", ScopeID: "second", Protected: false}))
	preview, err = s.Preview(t.Context(), request, "")
	testutil.FailErr(t, "preview eligible owner group", err)
	if preview.EligibleCount != 2 || preview.ReclaimableBytes != 10 {
		t.Fatalf("shared object counted incorrectly: %+v", preview)
	}
	request.PreviewToken = preview.Token
	result, err := s.Prune(t.Context(), request, "")
	testutil.FailErr(t, "prune eligible group", err)
	if result.RemovedCount != 2 || result.ReleasedBytes != 10 {
		t.Fatalf("group result: %+v", result)
	}
	var refs, tombstones int
	testutil.FailErr(t, "count remaining refs", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM checkpoint_object_refs`).Scan(&refs))
	testutil.FailErr(t, "count checkpoint tombstones", s.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM checkpoint_anchors WHERE pruned_at!=''`).Scan(&tombstones))
	if refs != 0 || tombstones != 2 {
		t.Fatalf("group release incomplete: refs=%d tombstones=%d", refs, tombstones)
	}
}
