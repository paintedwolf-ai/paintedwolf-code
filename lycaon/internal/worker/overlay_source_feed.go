package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

// promoteTarget is one candidate path's project-root state before an overlay lands.
type promoteTarget struct {
	rootID    string
	relPath   string
	abs       string
	existed   bool
	before    []byte
	beforeSHA string
}

// snapshotPromoteTargets captures promotion pre-images in attached roots.
func (s *MergeService) snapshotPromoteTargets(
	task *api.WorkerTask,
	roots []projectroot.RootRef,
	paths []string,
) (map[string]promoteTarget, error) {
	if task == nil || strings.TrimSpace(task.ProjectID) == "" || len(paths) == 0 {
		return nil, nil
	}
	out := make(map[string]promoteTarget, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		abs, root, err := projectroot.ResolveAbs(roots, task.WorkspaceRootID, p)
		if err != nil {
			continue
		}
		t := promoteTarget{rootID: root.ID, relPath: projectroot.ScopeRel(root, abs), abs: abs}
		info, statErr := os.Stat(abs)
		switch {
		case statErr == nil && !info.IsDir():
			t.existed = true
			before, beforeSHA, readErr := readForLedger(abs)
			if readErr != nil {
				return nil, fmt.Errorf("read promotion pre-image %s: %w", t.relPath, readErr)
			}
			t.before = before
			t.beforeSHA = beforeSHA
		case statErr != nil && !os.IsNotExist(statErr):
			return nil, fmt.Errorf("stat promotion target %s: %w", t.relPath, statErr)
		}
		out[p] = t
	}
	return out, nil
}

type promotionSourceFacts struct {
	records   []sourceledger.RecordInput
	changes   []sourcefeed.Change
	files     []api.FileEditSnapshot
	documents *promotedDocuments
}

// promotedDocuments carries the editor hold and the imports it stages at commit.
type promotedDocuments struct {
	hold  editordoc.PromotedDocumentHold
	syncs []editordoc.PromotedDocumentSync
}

// holdPromotedDocuments reserves open documents at the landing paths before any bytes land.
func (s *MergeService) holdPromotedDocuments(ctx context.Context, task *api.WorkerTask, targets map[string]promoteTarget, applied []string) (*promotedDocuments, error) {
	if s.Documents == nil || s.Projects == nil || task == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil, nil
	}
	refs := make([]editordoc.PathRef, 0, len(applied))
	for _, raw := range applied {
		if target, ok := targets[strings.TrimSpace(raw)]; ok && target.abs != "" {
			refs = append(refs, editordoc.PathRef{RootID: target.rootID, Path: target.relPath})
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	p, err := s.Projects.Get(ctx, strings.TrimSpace(task.ProjectID))
	if err != nil || p == nil {
		return nil, err
	}
	hold, err := s.Documents.HoldPromotedDocuments(ctx, p, refs)
	if err != nil {
		return nil, fmt.Errorf("hold promoted editor documents: %w", err)
	}
	return &promotedDocuments{hold: hold}, nil
}

// stageDocuments imports the landed bytes into held documents and attaches their text states to the records.
func (f *promotionSourceFacts) stageDocuments(ctx context.Context) {
	if f.documents == nil || f.documents.hold == nil {
		return
	}
	results := f.documents.hold.Stage(ctx, f.documents.syncs)
	for i := range f.records {
		result, ok := results[editordoc.PathRef{RootID: f.records[i].RootID, Path: f.records[i].Path}]
		if ok {
			f.records[i].TextBefore, f.records[i].TextAfter = result.TextBefore, result.TextAfter
		}
	}
}

func (f *promotionSourceFacts) documentWriter() PromotedDocumentWriter {
	if f.documents == nil || f.documents.hold == nil {
		return nil
	}
	return f.documents.hold
}

// release settles the hold; the first call decides whether staged imports publish.
func (d *promotedDocuments) release(ctx context.Context, committed bool) {
	if d != nil && d.hold != nil {
		d.hold.Release(ctx, committed)
	}
}

// promotedSourceFacts captures the landed bytes before the promotion commit;
// held editor documents are staged only when that commit is imminent.
func (s *MergeService) promotedSourceFacts(
	ctx context.Context,
	sessionID, toolCallID string,
	workspaceID string,
	userTurn int,
	task *api.WorkerTask,
	targets map[string]promoteTarget,
	applied []string,
	documents *promotedDocuments,
) (promotionSourceFacts, error) {
	if task == nil || len(applied) == 0 || len(targets) == 0 {
		return promotionSourceFacts{documents: documents}, nil
	}
	projectID := strings.TrimSpace(task.ProjectID)
	if projectID == "" {
		return promotionSourceFacts{documents: documents}, nil
	}
	ordered := orderAppliedByFirstWrite(ctx, s.SourceLedger, projectID, task.ID, applied)
	txID := uuid.NewString()
	facts := promotionSourceFacts{
		records:   make([]sourceledger.RecordInput, 0, len(ordered)),
		changes:   make([]sourcefeed.Change, 0, len(ordered)),
		documents: documents,
	}
	for _, raw := range ordered {
		target, ok := targets[strings.TrimSpace(raw)]
		if !ok || target.abs == "" {
			continue
		}
		after, afterSHA, err := readForLedger(target.abs)
		if err == nil && target.existed && afterSHA == target.beforeSHA && (target.before == nil || after == nil || bytes.Equal(after, target.before)) {
			continue
		}
		if os.IsNotExist(err) && !target.existed {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return promotionSourceFacts{}, fmt.Errorf("read promoted source %s: %w", target.relPath, err)
		}
		op := api.SourceChangeOpWrite
		switch {
		case err == nil && !target.existed:
			op = api.SourceChangeOpCreate
		case os.IsNotExist(err):
			op = api.SourceChangeOpDelete
			after, afterSHA = nil, ""
		}
		record := sourceledger.RecordInput{
			ProjectID: projectID,
			RootID:    target.rootID, Path: target.relPath, EntryKind: sourceledger.EntryKindFile,
			Op: op, Origin: api.SourceChangeOriginAgent,
			SessionID: sessionID, JobID: task.ID, ToolCallID: toolCallID,
			ToolName: "promote_overlay",
			Turn:     userTurn, OperationID: txID,
			Cause:        sourceledger.CauseOverlayPromote,
			BeforeSHA256: target.beforeSHA, AfterSHA256: afterSHA,
			Before: target.before, After: after,
			BeforeSize: int64(len(target.before)), AfterSize: int64(len(after)),
		}
		if documents != nil {
			documents.syncs = append(documents.syncs, editordoc.PromotedDocumentSync{
				RootID: target.rootID, Path: target.relPath,
				SessionID: sessionID, JobID: task.ID, Turn: userTurn,
				ToolCallID: toolCallID, ToolName: "promote_overlay",
				Deleted: op == api.SourceChangeOpDelete,
			})
		}
		if s.SourceLedger != nil {
			if resolver, ok := s.SourceLedger.(sourceledger.JobVersionResolver); ok {
				// The record points at merged bytes while retaining worker attribution.
				record.FileID, record.DerivedFromVersionID, _ = resolver.JobVersionForPath(
					ctx, projectID, task.ID, target.rootID, target.relPath,
				)
			}
			facts.records = append(facts.records, record)
		}
		if snapshot := promotedFileSnapshot(target, after, afterSHA, op); snapshot != nil {
			facts.files = append(facts.files, *snapshot)
		}
		facts.changes = append(facts.changes, promotedChange(record, workspaceID, target.abs))
	}
	return facts, nil
}

// promotedChange announces a landing with the attribution its ledger record carries.
func promotedChange(record sourceledger.RecordInput, workspaceID, abs string) sourcefeed.Change {
	return sourcefeed.Change{
		ProjectID: record.ProjectID, WorkspaceID: workspaceID, RootID: record.RootID, Path: record.Path,
		WorkspaceKind: api.SourceWorkspaceKindProject,
		Op:            record.Op, Origin: record.Origin,
		SessionID: record.SessionID, JobID: record.JobID, Turn: record.Turn, ToolCallID: record.ToolCallID,
		AfterSHA256: record.AfterSHA256, AbsPath: abs,
	}
}

// Text snapshots require retained, decodable bytes.
func promotedFileSnapshot(target promoteTarget, after []byte, afterSHA string, op api.SourceChangeOp) *api.FileEditSnapshot {
	if (target.existed && target.before == nil) || (afterSHA != "" && after == nil) {
		return nil
	}
	limits := textfile.LimitsForRaw(sourceledger.MaxRevisionContentBytes)
	beforeDoc, _, beforeErr := textfile.Open(target.before, limits)
	afterDoc, _, afterErr := textfile.Open(after, limits)
	if beforeErr != nil || afterErr != nil {
		return nil
	}
	snapshot := &api.FileEditSnapshot{
		RootID: target.rootID, Path: target.relPath, After: afterDoc.Text(),
		Deleted: op == api.SourceChangeOpDelete,
	}
	if target.existed {
		before := beforeDoc.Text()
		snapshot.Before = &before
	}
	return snapshot
}

// orderAppliedByFirstWrite preserves worker write order.
func orderAppliedByFirstWrite(
	ctx context.Context,
	ledger sourceledger.PromoteRecorder,
	projectID, jobID string,
	applied []string,
) []string {
	if len(applied) == 0 {
		return nil
	}
	pending := make([]string, 0, len(applied))
	seen := make(map[string]struct{}, len(applied))
	for _, raw := range applied {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		pending = append(pending, p)
	}
	if ledger == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(jobID) == "" {
		return pending
	}
	firstWrite, err := ledger.JobPathFirstWriteOrder(ctx, projectID, jobID)
	if err != nil || len(firstWrite) == 0 {
		return pending
	}
	out := make([]string, 0, len(pending))
	claimed := make(map[string]struct{}, len(pending))
	for _, p := range firstWrite {
		p = strings.TrimSpace(p)
		if _, ok := seen[p]; !ok {
			continue
		}
		if _, ok := claimed[p]; ok {
			continue
		}
		claimed[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range pending {
		if _, ok := claimed[p]; ok {
			continue
		}
		out = append(out, p)
	}
	return out
}

// readForLedger hashes large files without retaining their content.
func readForLedger(abs string) (content []byte, sha string, err error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, "", err
	}
	if info.Size() <= sourceledger.MaxRevisionContentBytes {
		b, readErr := os.ReadFile(abs)
		if readErr != nil {
			return nil, "", readErr
		}
		sum := sha256.Sum256(b)
		return b, hex.EncodeToString(sum[:]), nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, "", err
	}
	return nil, hex.EncodeToString(h.Sum(nil)), nil
}
