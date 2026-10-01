package workercompletion_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParentProofFromOmitsHostLedger(t *testing.T) {
	declaredCommand := "check"
	host := workercompletion.WorkerCompletionProof{
		ChangedPaths:      []string{"src/a.go"},
		WorkspaceDirty:    true,
		MutationTools:     []string{"write"},
		SurveyTools:       []string{"grep"},
		ReceiptCount:      86,
		VisualArtifactIDs: []string{"art-1"},
		InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
			ID:               "6bef141c-dd8d-480c-b428-edb5338297fd",
			Tool:             "grep",
			Status:           api.InvocationStatusCompleted,
			EvidenceKind:     "result",
			EvidenceRef:      "fb4779ad-d68e-42af-94ca-71702963f597",
			SourceRevision:   "223bb346-4a7d-4325-8023-7855fffc2f54:240",
			SourceRootDigest: "f7945ae9afbd0dfcd5469ff89715fb0e",
		}},
		SourceRevision:   "223bb346-4a7d-4325-8023-7855fffc2f54:240",
		SourceRootDigest: "f7945ae9afbd0dfcd5469ff89715fb0e",
		DeclaredCommand:  &declaredCommand,
	}
	parent := workercompletion.ParentProofFrom(host)
	if parent.Empty() {
		t.Fatal("expected parent fields")
	}
	if parent.ReceiptCount != 86 || len(parent.ChangedPaths) != 1 || parent.ChangedPaths[0] != "src/a.go" {
		t.Fatalf("parent = %+v", parent)
	}
	raw, err := json.Marshal(parent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, banned := range []string{
		"invocation_receipts", "source_revision", "source_root_digest",
		"declared_command", "6bef141c", "f7945ae9",
	} {
		if strings.Contains(s, banned) {
			t.Fatalf("parent proof leaked %q: %s", banned, s)
		}
	}
	round := parent.Proof()
	if len(round.InvocationReceipts) != 0 || round.SourceRevision != "" || round.DeclaredCommand != nil {
		t.Fatalf("Proof() = %+v", round)
	}
}

func TestParentProofEmpty(t *testing.T) {
	if !(workercompletion.ParentProof{}).Empty() {
		t.Fatal("zero value should be empty")
	}
	if (workercompletion.ParentProof{ReceiptCount: 1}).Empty() {
		t.Fatal("receipt_count should be non-empty")
	}
}

func TestParentProofParseIgnoresReceiptJSON(t *testing.T) {
	const raw = `{"changed_paths":["a.go"],"receipt_count":2,"invocation_receipts":[{"id":"r1","tool":"grep"}],"source_revision":"rev"}`
	var parent workercompletion.ParentProof
	if err := json.Unmarshal([]byte(raw), &parent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parent.ChangedPaths) != 1 || parent.ReceiptCount != 2 {
		t.Fatalf("parent = %+v", parent)
	}
	proof := parent.Proof()
	if len(proof.InvocationReceipts) != 0 || proof.SourceRevision != "" {
		t.Fatalf("Proof() = %+v", proof)
	}
}
