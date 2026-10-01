package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxAgentBatchDocuments = 256

type agentAcceptance struct {
	input    AgentEdit
	document *Document
	project  *project.Project
	plan     *agentPlan
	previous int64
	mutation *Mutation
}

// ApplyAgentEdits validates all anchors before accepting any agent text. The
// accepted document heads and publication intents share one writer transaction.
// Results retain input order; publication failures belong to individual files.
func (s *Service) ApplyAgentEdits(ctx context.Context, inputs []AgentEdit) ([]*AgentEditResult, error) {
	if len(inputs) == 0 {
		return []*AgentEditResult{}, nil
	}
	keys, err := agentBatchKeys(inputs)
	if err != nil {
		return nil, err
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.lockDocuments(keys)
	defer unlock()
	batch, replays, err := s.planAgentBatch(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if replays {
		return s.replayAgentBatch(ctx, batch)
	}
	releases := make([]func(), 0, len(batch))
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, item := range batch {
		release, err := s.reserveDocumentSource(ctx, item.project, item.document)
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	for _, item := range batch {
		if err := s.prepareAgentAcceptance(ctx, item); err != nil {
			s.recoverHistoryCapacity(ctx, item.document.ID, err)
			return nil, err
		}
	}
	if err := s.store.Tx(ctx, func(tx *sql.Tx) error {
		for _, item := range batch {
			if err := s.commitAgentAcceptance(ctx, tx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, item := range batch {
		s.changed(ctx, item.document, true)
	}
	results := make([]*AgentEditResult, len(batch))
	for i, item := range batch {
		results[i] = s.publishAgentAcceptance(ctx, item)
	}
	return results, nil
}

func agentBatchKeys(inputs []AgentEdit) ([]string, error) {
	if len(inputs) > maxAgentBatchDocuments {
		return nil, errors.New("too many documents in one edit transaction")
	}
	keys := make([]string, 0, len(inputs))
	ids := make(map[string]bool, len(inputs))
	operations := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.OperationID) == "" || in.DocumentID == "" || ids[in.DocumentID] || operations[in.OperationID] || in.ProjectID != inputs[0].ProjectID {
			return nil, ErrOperationConflict
		}
		ids[in.DocumentID], operations[in.OperationID] = true, true
		keys = append(keys, in.DocumentID)
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *Service) planAgentBatch(ctx context.Context, inputs []AgentEdit) ([]*agentAcceptance, bool, error) {
	batch := make([]*agentAcceptance, len(inputs))
	replays := 0
	for i, in := range inputs {
		d, err := s.checked(ctx, in.DocumentID, in.ProjectID)
		if err != nil {
			return nil, false, err
		}
		p, err := s.projectBranch(ctx, d.ProjectID, d.BranchID)
		if err != nil {
			return nil, false, err
		}
		batch[i] = &agentAcceptance{input: in, document: d, project: p}
		var digest string
		err = s.store.db.QueryRowContext(ctx, `SELECT input_digest FROM editor_agent_receipts WHERE document_id=? AND operation_id=?`, d.ID, in.OperationID).Scan(&digest)
		if err == nil {
			inputDigest, err := agentInputDigest(in)
			if err != nil {
				return nil, false, err
			}
			if digest != inputDigest {
				return nil, false, ErrOperationConflict
			}
			replays++
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if replays > 0 {
		if replays != len(inputs) {
			return nil, false, ErrOperationConflict
		}
		return batch, true, nil
	}
	for _, item := range batch {
		if item.input.ReviewedRevision != 0 && item.input.ReviewedRevision != item.document.Revision {
			return nil, false, ErrRevisionConflict
		}
		plan, err := s.planAgentEdit(ctx, item.document, item.input)
		if err != nil {
			return nil, false, err
		}
		item.plan = plan
	}
	return batch, false, nil
}

func (s *Service) replayAgentBatch(ctx context.Context, batch []*agentAcceptance) ([]*AgentEditResult, error) {
	results := make([]*AgentEditResult, len(batch))
	for i, item := range batch {
		result, err := s.replayAgentEdit(ctx, item.document, item.input)
		if err != nil {
			return nil, err
		}
		results[i] = result
	}
	return results, nil
}

func (s *Service) prepareAgentAcceptance(ctx context.Context, item *agentAcceptance) error {
	d := item.document
	item.previous = d.Revision
	d.Draft, d.plannedEdits = item.plan.content, item.plan.edits
	d.Dirty = d.Draft != d.BaseContent || d.EOL != d.BaseEOL || d.MixedEOL != d.BaseMixedEOL
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	d.authorship = authorshipContext{sessionID: item.input.SessionID, turn: item.input.Turn, toolCallID: item.input.ToolCallID, toolName: item.input.ToolName}
	if err := s.prepareTextTransition(ctx, d, item.previous, textActor{kind: actorAgent}, item.input.OperationID); err != nil {
		return err
	}
	if d.Dirty && !d.Diverged {
		in := item.input
		mutation, err := s.prepareSaveMutation(ctx, d, in.OperationID, saveActor{Origin: api.SourceChangeOriginAgent,
			SessionID: strings.TrimSpace(in.SessionID), Turn: in.Turn, ToolCallID: strings.TrimSpace(in.ToolCallID), ToolName: strings.TrimSpace(in.ToolName)})
		if err != nil {
			return err
		}
		item.mutation = mutation
	}
	return nil
}

func (s *Service) commitAgentAcceptance(ctx context.Context, tx *sql.Tx, item *agentAcceptance) error {
	d := item.document
	if d.Dirty && d.Diverged {
		held, err := s.retainAgentEditTx(ctx, tx, d, item.input)
		if err != nil {
			return err
		}
		d.HeldAgentVersionID = held
	}
	if err := s.store.UpdateCASTx(ctx, tx, d, item.previous); err != nil {
		return err
	}
	if item.mutation != nil {
		if err := s.store.InsertMutationTx(ctx, tx, item.mutation); err != nil {
			return err
		}
	}
	return recordAgentReceipt(ctx, tx, d.ID, item.input, d.HeldAgentVersionID, item.mutation != nil)
}

func (s *Service) publishAgentAcceptance(ctx context.Context, item *agentAcceptance) *AgentEditResult {
	d := item.document
	result := &AgentEditResult{Document: s.withParticipants(d), Saved: !d.Dirty && !d.Diverged, HeldVersionID: d.HeldAgentVersionID}
	if item.mutation == nil {
		return result
	}
	saved, err := s.finishMutation(ctx, item.project, d, item.mutation)
	if err == nil {
		result.Document, result.Saved = saved, true
		return result
	}
	current, getErr := s.store.Get(ctx, d.ID)
	if getErr != nil {
		result.PublicationError = errors.Join(err, getErr)
		return result
	}
	if projectionErr := s.replicaProjection(ctx, current, nil); projectionErr != nil {
		result.PublicationError = errors.Join(err, projectionErr)
		return result
	}
	result.Document, result.HeldVersionID = s.withParticipants(current), current.HeldAgentVersionID
	if !errors.Is(err, project.ErrSourceWriteConflict) || result.HeldVersionID == "" {
		result.PublicationError = err
	}
	return result
}
