package store

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommitEvidenceToolResult_filmstripMintsFrameHandles(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, "proj")
	testutil.FailErr(t, "create session in store", err)
	body := `{"capture":"filmstrip","mime":"application/vnd.lycaon.filmstrip+zip","log":[],"state":{},"snapshot":{},"frames":[
		{"index":0,"caption":"initial","state":{"n":0},"snapshot":{"text":"empty"}},
		{"index":1,"caption":"click #load","state":{"n":1},"snapshot":{"text":"gamma"}}
	]}`
	handle, patched, err := store.CommitEvidenceToolResult(ctx, sess.ID, "/tmp", "capture_page", map[string]any{"capture": "filmstrip"}, body)
	testutil.FailErr(t, "store.CommitEvidenceToolResult failed", err)
	if handle != "page#3" {
		t.Fatalf("parent handle=%q want page#3 (2 frames + parent)", handle)
	}
	if !strings.Contains(patched, `"evidence_handle":"page#1"`) || !strings.Contains(patched, `"evidence_handle":"page#2"`) {
		t.Fatalf("patched frames: %s", patched)
	}
	ledger, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "store.LoadLedger failed", err)
	fr1, ok := ledger.Handles["page#1"]
	if !ok || fr1.FrameIndex != 1 {
		t.Fatalf("page#1 = %#v", fr1)
	}
	fr2, ok := ledger.Handles["page#2"]
	if !ok || fr2.FrameIndex != 2 {
		t.Fatalf("page#2 = %#v", fr2)
	}
	parent, ok := ledger.Handles["page#3"]
	if !ok || parent.FrameIndex != 0 {
		t.Fatalf("parent = %#v", parent)
	}
	okVerify, _ := evidence.VerifyRecord(fr2, evidence.Claim{Excerpt: `"text":"gamma"`})
	if !okVerify {
		t.Fatal("frame 2 body should verify gamma")
	}
}
