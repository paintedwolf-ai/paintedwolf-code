package historyretention

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClassUsageCountsSharedContentOnceAndIncludesRetainedReceipts(t *testing.T) {
	s := newTestService(t)
	sha := strings.Repeat("a", 64)
	_, err := s.Database.ExecContext(t.Context(), `INSERT INTO source_blob_objects(sha256,size,stored_size,storage_relpath,git_oid_sha1,git_oid_sha256) VALUES(?,100,10,'body.zst','','')`, sha)
	testutil.FailErr(t, "seed shared content", err)
	for _, anchor := range []string{"first", "second"} {
		_, err = s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_anchors(session_id,anchor_id,project_id,root_key,sealed_at,manifest_json) VALUES('session',?,?,'root','2026-10-08T00:00:00Z','{}')`, anchor, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "seed anchor", err)
		_, err = s.Database.ExecContext(t.Context(), `INSERT INTO checkpoint_object_refs(session_id,anchor_id,path,sha256,original_size,mode) VALUES('session',?,'shared.go',?,100,420)`, anchor, sha)
		testutil.FailErr(t, "reference shared content", err)
	}
	seedReceipts(t, s, 1)
	_, err = s.Database.ExecContext(t.Context(), `UPDATE llm_calls SET rate_snapshot='{}',provider_id='π',model='é',caller=''`)
	testutil.FailErr(t, "set unicode receipt detail", err)
	testutil.FailErr(t, "refresh class inventory", s.refreshUsage(t.Context()))
	status, err := s.Status(t.Context())
	testutil.FailErr(t, "read status", err)
	if len(status.Classes) != 5 {
		t.Fatalf("class count=%d want 5", len(status.Classes))
	}
	for _, usage := range status.Classes {
		want := int64(0)
		if usage.Class == "checkpoints" {
			want = 100
		}
		if usage.Class == "receipt_detail" {
			want = 6
		}
		if usage.ContentBytes != want {
			t.Errorf("class %s bytes=%d want %d", usage.Class, usage.ContentBytes, want)
		}
	}
	if status.Policy.Recordings.Mode != "forever" || status.Policy.ReceiptDetail.Mode != "forever" {
		t.Fatal("measurement changed the default retention policy")
	}
}
