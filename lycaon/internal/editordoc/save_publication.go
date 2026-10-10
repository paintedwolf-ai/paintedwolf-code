package editordoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

// Save publishes an exact accepted snapshot and deduplicates operation retries.
func (s *Service) Save(ctx context.Context, p *project.Project, id, clientID, operationID, sessionID string, turn int, expected int64) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	saver, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return nil, err
	}
	actor := saveActor{Origin: api.SourceChangeOriginUser, PersonID: saver.personID, ClientID: saver.clientID,
		SessionID: strings.TrimSpace(sessionID), Turn: turn}
	replay, err := s.findSaveReplay(ctx, p, id, operationID, actor, expected)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return s.resumeSave(ctx, p, replay)
	}
	current, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	reserved, err := s.store.savePinRevision(ctx, id, actor.ClientID, strings.TrimSpace(operationID))
	if err != nil {
		return nil, err
	}
	if reserved > 0 && reserved != expected {
		return nil, ErrOperationConflict
	}
	d := current
	if reserved > 0 || current.Revision != expected {
		d, err = s.pinnedDocument(ctx, current, expected)
		if err != nil {
			return nil, err
		}
		// A reservation behind the saved base has nothing left to publish.
		if d.BaseSHA256 != current.BaseSHA256 || d.Absent != current.Absent {
			err := ErrRevisionConflict
			if reserved > 0 {
				if settleErr := s.settleRejectedReservation(ctx, d, operationID, actor, err); settleErr != nil {
					return nil, settleErr
				}
			}
			return nil, err
		}
	}
	result, err := s.saveLocked(ctx, p, d, operationID, actor)
	if err != nil && reserved > 0 {
		if settleErr := s.settleRejectedReservation(ctx, d, operationID, actor, err); settleErr != nil {
			return nil, settleErr
		}
	}
	return result, err
}

type saveReplay struct {
	document *Document
	mutation *Mutation
}

// findSaveReplay validates reused input; nil means the operation ID is unused.
func (s *Service) findSaveReplay(ctx context.Context, p *project.Project, id, operationID string, actor saveActor, expected int64) (*saveReplay, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, fmt.Errorf("operation id required")
	}
	inputDigest := saveInputDigest(strings.TrimSpace(id), strings.TrimSpace(p.ID), actor, expected)
	existing, getErr := s.store.Mutation(ctx, operationID)
	if errors.Is(getErr, ErrNotFound) {
		return nil, nil
	}
	if getErr != nil {
		return nil, getErr
	}
	if existing.InputDigest != inputDigest {
		return nil, ErrOperationConflict
	}
	d, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	return &saveReplay{document: d, mutation: existing}, nil
}

func (s *Service) resumeSave(ctx context.Context, p *project.Project, replay *saveReplay) (*Document, error) {
	if err := checkWorkspace(p, replay.document); err != nil {
		return nil, err
	}
	if replay.mutation.Status == "complete" {
		return s.mutationResponse(ctx, replay.document, replay.mutation)
	}
	release, err := s.reserveDocumentSource(ctx, p, replay.document)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.finishMutation(ctx, p, replay.document, replay.mutation)
}

// saveLocked journals and applies one save of the document's current draft.
// The caller holds the document lock and has settled who is saving.
func (s *Service) saveLocked(ctx context.Context, p *project.Project, d *Document, operationID string, actor saveActor) (*Document, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, err
	}
	release, err := s.reserveDocumentSource(ctx, p, d)
	if err != nil {
		return nil, err
	}
	defer release()
	if len(d.CRDTUpdate) == 0 {
		if err := s.replicaProjection(ctx, d, nil); err != nil {
			return nil, err
		}
	}
	m, err := s.prepareSaveMutation(ctx, d, operationID, actor)
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertMutation(ctx, m); err != nil {
		return nil, err
	}
	return s.finishMutation(ctx, p, d, m)
}

// prepareSaveMutation journals the exact bytes one save will publish. An
// absent document expects no file, so its mutation carries an empty hash.
func (s *Service) prepareSaveMutation(ctx context.Context, d *Document, operationID string, actor saveActor) (*Mutation, error) {
	checkpoint, err := s.publicationCheckpoint(ctx, d)
	if err != nil {
		return nil, err
	}
	head, err := s.store.replicaHead(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	content := serializeEOL(d.Draft, d.EOL)
	after, err := textfile.EncodeBounded(content, d.Encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes))
	if err != nil {
		return nil, err
	}
	root, err := s.liveRootPath(ctx, d.ProjectID, d.BranchID, d.RootID)
	if err != nil {
		return nil, err
	}
	var before []byte
	expected := d.BaseSHA256
	if d.Absent {
		expected = ""
	} else if before, err = readPublicationPreimage(root, d.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC()
	m := &Mutation{ID: strings.TrimSpace(operationID), BranchID: d.BranchID, ClientID: actor.ClientID,
		InputDigest: saveInputDigest(d.ID, d.ProjectID, actor, d.Revision),
		DocumentID:  d.ID, ProjectID: d.ProjectID, FileID: d.FileID,
		RootID: d.RootID, Path: d.Path, ExpectedSHA256: expected,
		AfterSHA256: textfile.SHA256(after), Encoding: d.Encoding, Content: content,
		DraftRevision: d.Revision, EOL: d.EOL, Checkpoint: checkpoint, BeforeCheckpoint: head.PublishedCheckpoint,
		BeforeBytes: before, AfterBytes: after, Status: "prepared",
		SessionID: actor.SessionID, Turn: actor.Turn, CreatedAt: now, UpdatedAt: now,
		Origin: actor.Origin, PersonID: actor.PersonID, ToolCallID: actor.ToolCallID, ToolName: actor.ToolName}
	return m, nil
}

func (s *Service) finishMutation(ctx context.Context, p *project.Project, d *Document, m *Mutation) (*Document, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, err
	}
	if m.BranchID != d.BranchID {
		return nil, ErrNotFound
	}
	current, err := s.store.Get(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	d = current
	if m.Status == "prepared" {
		result, err := publishMutation(p, m)
		if err != nil {
			status := "failed"
			if errors.Is(err, projectsource.ErrSourceWriteConflict) {
				status = "conflict"
			}
			if settleErr := s.settlePublicationFailure(ctx, d, m, status, err); settleErr != nil {
				return nil, errors.Join(err, settleErr)
			}
			return nil, err
		}
		m.AfterSHA256, m.BeforeBytes, m.AfterBytes = result.SHA256, result.Before, result.After
		m.Status, m.UpdatedAt = "file_applied", time.Now().UTC()
		if err := s.store.UpdateMutation(ctx, m); err != nil {
			return nil, err
		}
	}
	if m.Status != "file_applied" && m.Status != "complete" {
		return nil, ErrRevisionConflict
	}
	if m.Status == "file_applied" {
		if err := s.commit(ctx, p, d, m); err != nil {
			return nil, err
		}
	}
	return s.mutationResponse(ctx, d, m)
}

// publishMutation lands the journaled bytes: an exclusive create when the
// document expected no file, a guarded replacement otherwise.
func publishMutation(p *project.Project, m *Mutation) (*projectsource.SourceWriteResult, error) {
	req := projectsource.SourceWriteRequest{Path: m.Path, RootID: m.RootID, Content: m.Content, Encoding: m.Encoding, BaseSHA256: m.ExpectedSHA256}
	if m.Creates() {
		return projectsource.ApplySourceWriteCreate(p, req)
	}
	return projectsource.ApplySourceWriteCAS(p, req)
}

// Creates reports a publication that expects no file at its path.
func (m *Mutation) Creates() bool { return m.ExpectedSHA256 == "" }

// commit atomically records attribution, events, document state, and completion.
func (s *Service) commit(ctx context.Context, p *project.Project, d *Document, m *Mutation) error {
	if err := s.replicaProjection(ctx, d, nil); err != nil {
		return err
	}
	next := *d
	next.replicaCommit = nil
	next.PublishedRevision = m.DraftRevision
	next.publishedCheckpoint = m.Checkpoint
	next.BaseContent, next.BaseSHA256 = strings.ReplaceAll(m.Content, "\r\n", "\n"), m.AfterSHA256
	next.SizeBytes = int64(len(m.AfterBytes))
	next.BaseEOL, next.BaseMixedEOL = m.EOL, false
	next.Diverged = !s.mutationMatchesDisk(ctx, m)
	next.Absent = false
	if d.Revision == m.DraftRevision {
		next.MixedEOL = false
	}
	next.Dirty = next.Draft != next.BaseContent || next.EOL != next.BaseEOL || next.MixedEOL != next.BaseMixedEOL
	// The saved draft resolves the pending agent edit.
	next.HeldAgentVersionID = ""
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	done := *m
	done.Checkpoint = nil
	done.BeforeCheckpoint = nil
	done.Status, done.Error, done.Content, done.BeforeBytes, done.AfterBytes = "complete", "", "", nil, nil
	done.UpdatedAt = time.Now().UTC()
	op := api.SourceChangeOpWrite
	if m.Creates() {
		op = api.SourceChangeOpCreate
	}
	change := sourcefeed.Change{
		ProjectID: d.ProjectID, WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID: d.RootID, Path: d.Path, Op: op, Origin: m.Origin,
		SessionID: m.SessionID, Turn: m.Turn, ToolCallID: m.ToolCallID, AfterSHA256: m.AfterSHA256,
		AbsPath: filepath.Join(rootPath(p, d.RootID), filepath.FromSlash(d.Path)),
	}
	var delivery *sourcefeed.StagedDelivery
	textAfter, err := s.publicationTextState(ctx, d, m.Checkpoint)
	if err != nil {
		return err
	}
	if textAfter != nil {
		textAfter.Revision = m.DraftRevision
	}
	record := sourceledger.RecordInput{
		TextAfter: textAfter,
		ProjectID: m.ProjectID, BranchID: d.BranchID,
		RootID: m.RootID, Path: m.Path, FileID: d.FileID, EntryKind: sourceledger.EntryKindFile,
		Op: op, Origin: m.Origin, PersonID: m.PersonID,
		SessionID: m.SessionID, Turn: m.Turn, OperationID: m.ID,
		ToolCallID: m.ToolCallID, ToolName: m.ToolName,
		AfterSHA256: m.AfterSHA256, After: m.AfterBytes, AfterSize: int64(len(m.AfterBytes)),
	}
	if m.Creates() {
		// A recreated path is a new file to the ledger.
		record.FileID = ""
	} else {
		textBefore, err := s.publicationTextState(ctx, d, m.BeforeCheckpoint)
		if err != nil {
			return err
		}
		if textBefore != nil {
			textBefore.Revision = d.PublishedRevision
		}
		record.TextBefore = textBefore
		record.Before, record.BeforeSize = m.BeforeBytes, int64(len(m.BeforeBytes))
	}
	err = s.store.Tx(ctx, func(tx *sql.Tx) error {
		tracked, err := s.ledger.RecordFileTx(ctx, tx, record)
		if err != nil {
			return err
		}
		if tracked.FileID != "" {
			next.FileID = tracked.FileID
		}
		delivery, err = sourcefeed.EmitTx(ctx, tx, change)
		if err != nil {
			return err
		}
		if err := s.store.UpdateCASTx(ctx, tx, &next, d.Revision); err != nil {
			return err
		}
		response := s.withParticipants(&next)
		responseJSON, err := json.Marshal(response)
		if err != nil {
			return err
		}
		done.ResponseJSON = string(responseJSON)
		return s.store.UpdateMutationTx(ctx, tx, &done)
	})
	if err != nil {
		return err
	}
	*m = done
	*d = next
	delivery.DeliverCommitted()
	s.changed(ctx, d, false)
	return nil
}

// Compact receipts prevent duplicate publication on reconnect.
func (s *Service) mutationResponse(ctx context.Context, current *Document, m *Mutation) (*Document, error) {
	if m.ReplayCompacted {
		if err := s.replicaProjection(ctx, current, nil); err != nil {
			return nil, err
		}
		return s.withParticipants(current), nil
	}
	return decodeMutationResponse(m)
}

func decodeMutationResponse(m *Mutation) (*Document, error) {
	if strings.TrimSpace(m.ResponseJSON) == "" {
		return nil, fmt.Errorf("editor save %s has no committed response", m.ID)
	}
	var response Document
	if err := json.Unmarshal([]byte(m.ResponseJSON), &response); err != nil {
		return nil, fmt.Errorf("decode editor save %s response: %w", m.ID, err)
	}
	return &response, nil
}

// A remembered byte order applies only while the bytes need explicit decoding.
