package editordoc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

type ConflictResolution struct {
	HistoryVector    []byte
	SessionID        string
	Turn             int `json:"-"`
	ClientID         string
	OperationID      string
	ExpectedRevision int64
	DiskSHA256       string
	Content          string
	EOL              string
}

// Resolve verifies both versions shown in the merge. Acceptance and the save
// intent commit together so interruption cannot strand a half-applied merge.
func (s *Service) Resolve(ctx context.Context, p *project.Project, id string, in ConflictResolution) (*Document, error) {
	if strings.TrimSpace(in.ClientID) == "" {
		return nil, ErrReplicaIdentity
	}
	if _, err := uuid.Parse(in.OperationID); err != nil {
		return nil, ErrOperationConflict
	}
	if in.EOL != "lf" && in.EOL != "crlf" {
		return nil, ErrInvalidEOL
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	encoded, err := json.Marshal(struct {
		DocumentID, ProjectID string
		Input                 ConflictResolution
	}{id, p.ID, in})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	inputDigest := "resolve:" + hex.EncodeToString(digest[:])
	d, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.Mutation(ctx, in.OperationID)
	if err == nil {
		if existing.InputDigest != inputDigest {
			return nil, ErrOperationConflict
		}
		result, resumeErr := s.resumeSave(ctx, p, &saveReplay{document: d, mutation: existing})
		if resumeErr != nil {
			return nil, resumeErr
		}
		if len(in.HistoryVector) > 0 {
			receipt, receiptErr := s.store.replicaReceipt(ctx, id, in.OperationID)
			if receiptErr != nil {
				return nil, receiptErr
			}
			result.CommandHistory = receipt.CommandHistory
		}
		return result, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if d.Revision != in.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	read, err := readDocumentSource(p, d)
	if err != nil {
		return nil, err
	}
	if err := validateEditableSource(read); err != nil {
		return nil, err
	}
	if read.SHA256 != in.DiskSHA256 {
		return nil, project.ErrSourceWriteConflict
	}
	if err := s.reconcileObservedDocument(ctx, d, read, false, in.ClientID, ""); err != nil {
		return nil, err
	}
	base, baseEOL, mixed := normalizeEOL(read.Content)
	previous := d.Revision
	d.Draft = strings.ReplaceAll(in.Content, "\r\n", "\n")
	d.EOL, d.MixedEOL = in.EOL, false
	d.BaseContent, d.BaseEOL, d.BaseMixedEOL = base, baseEOL, mixed
	d.BaseSHA256, d.Encoding, d.SizeBytes = read.SHA256, read.Encoding, read.SizeBytes
	d.Dirty = d.Draft != base || d.EOL != baseEOL || mixed
	d.Diverged = false
	d.HeldAgentVersionID = ""
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	d.historyVector = in.HistoryVector
	d.authorship = authorshipContext{sessionID: in.SessionID, turn: in.Turn}
	actor, err := s.personActor(ctx, actorRestore, in.ClientID)
	if err != nil {
		return nil, err
	}
	if err := s.prepareTextTransition(ctx, d, previous, actor, in.OperationID); err != nil {
		return nil, err
	}
	d.replicaCommit.inputDigest = inputDigest
	mutation, err := s.prepareSaveMutation(ctx, d, in.OperationID, saveActor{Origin: api.SourceChangeOriginUser, PersonID: actor.personID, ClientID: in.ClientID, SessionID: in.SessionID, Turn: in.Turn})
	if err != nil {
		return nil, err
	}
	mutation.InputDigest = inputDigest
	if err := s.store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.store.UpdateCASTx(ctx, tx, d, previous); err != nil {
			return err
		}
		return s.store.InsertMutationTx(ctx, tx, mutation)
	}); err != nil {
		return nil, err
	}
	s.changed(ctx, d, true)
	result, err := s.finishMutation(ctx, p, d, mutation)
	if err != nil {
		return nil, err
	}
	result.CommandHistory = d.CommandHistory
	return result, nil
}
