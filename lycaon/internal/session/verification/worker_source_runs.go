package verification

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerSourceRuns reads a child's terminal command results from the evidence store.
func (m *Service) WorkerSourceRuns(ctx context.Context, sessionID string) ([]workercompletion.WorkerInvocationReceipt, error) {
	if m == nil || m.evidenceStore == nil {
		return nil, nil
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return nil, err
	}
	runID := sessiontree.RootID(ctx, m.store, sessionID)
	records, err := m.evidenceStore.ReadAll(ctx, m.EvidenceRootFor(sess), runID, VerifySlot, evidence.GateTypeVerify)
	if err != nil {
		return nil, err
	}
	var out []workercompletion.WorkerInvocationReceipt
	for _, record := range records {
		owner, _ := record.Artifacts["session_id"].(string)
		id, _ := record.Artifacts["check_id"].(string)
		producer, _ := record.Artifacts["producer"].(string)
		if owner != sessionID || strings.TrimSpace(id) == "" || (producer != "verify" && producer != "command") {
			continue
		}
		command, _ := record.Artifacts["command"].(string)
		cwd, _ := record.Artifacts["cwd"].(string)
		revision, _ := record.Artifacts["source_revision"].(string)
		root, _ := record.Artifacts["source_root_digest"].(string)
		isCheck, _ := record.Artifacts["is_check"].(bool)
		out = append(out, workercompletion.WorkerInvocationReceipt{
			ID: "source-run:" + id, CheckID: id, Tool: producer, Command: command, Cwd: cwd, IsCheck: isCheck,
			Status: api.InvocationStatusCompleted, EvidenceKind: "source_run", Verdict: record.GateVerdict,
			SourceRevision: revision, SourceRootDigest: root,
		})
	}
	return out, nil
}
