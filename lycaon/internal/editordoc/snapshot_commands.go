package editordoc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/textfile"
)

type DocumentCommand struct {
	HistoryVector    []byte
	SessionID        string
	Turn             int
	ClientID         string
	OperationID      string
	ExpectedRevision int64
}

type SnapshotReplacement struct {
	DocumentCommand
	Content  string
	EOL      string
	MixedEOL bool
}

func commandDigest(action string, input any) (string, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return action + ":" + hex.EncodeToString(digest[:]), nil
}

func (s *Service) checkedSnapshotCommand(ctx context.Context, id, projectID string, in DocumentCommand, digest string) (*Document, bool, error) {
	if _, err := uuid.Parse(in.OperationID); err != nil {
		return nil, false, ErrOperationConflict
	}
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, false, err
	}
	receipt, err := s.store.replicaReceipt(ctx, id, in.OperationID)
	if err == nil {
		if receipt.InputDigest != digest {
			return nil, false, ErrOperationConflict
		}
		if err := s.replicaProjection(ctx, d, nil); err != nil {
			return nil, false, err
		}
		d.CommandHistory = receipt.CommandHistory
		return s.withParticipants(d), true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	d, err = s.checkedRevision(ctx, id, projectID, in.ClientID, in.ExpectedRevision)
	return d, false, err
}

func (s *Service) commitSnapshotCommand(ctx context.Context, d *Document, previous int64, kind string, in DocumentCommand, digest string) error {
	d.authorship = authorshipContext{sessionID: in.SessionID, turn: in.Turn}
	d.historyVector = in.HistoryVector
	actor := textActor{kind: kind, clientID: strings.TrimSpace(in.ClientID)}
	if kind != actorExternal {
		var err error
		if actor, err = s.personActor(ctx, kind, in.ClientID); err != nil {
			return err
		}
	}
	if err := s.prepareTextTransition(ctx, d, previous, actor, in.OperationID); err != nil {
		s.recoverHistoryCapacity(ctx, d.ID, err)
		return err
	}
	if kind == actorExternal {
		checkpoint, err := s.publicationCheckpoint(ctx, d)
		if err != nil {
			return err
		}
		d.publishedCheckpoint = checkpoint
		d.PublishedRevision = d.Revision
	}
	d.replicaCommit.inputDigest = digest
	return s.store.UpdateCAS(ctx, d, previous)
}

func (s *Service) ReplaceSnapshot(ctx context.Context, id, projectID string, in SnapshotReplacement) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	digest, err := commandDigest("replace", in)
	if err != nil {
		return nil, err
	}
	d, replayed, err := s.checkedSnapshotCommand(ctx, id, projectID, in.DocumentCommand, digest)
	if err != nil || replayed {
		return d, err
	}
	if in.EOL != "lf" && in.EOL != "crlf" {
		return nil, ErrInvalidEOL
	}
	limits := textfile.LimitsForRaw(project.SourceWriteMaxBytes)
	if int64(len(in.Content)) > limits.MaxTextBytes {
		return nil, textfile.ErrTextTooLarge
	}
	draft := strings.ReplaceAll(in.Content, "\r\n", "\n")
	if _, err := textfile.EncodeBounded(serializeEOL(draft, in.EOL), d.Encoding, limits); err != nil {
		return nil, err
	}
	previous := d.Revision
	d.Draft, d.EOL, d.MixedEOL = draft, in.EOL, in.MixedEOL
	d.Dirty = d.Draft != d.BaseContent || d.EOL != d.BaseEOL || d.MixedEOL != d.BaseMixedEOL
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	if err := s.commitSnapshotCommand(ctx, d, previous, actorUser, in.DocumentCommand, digest); err != nil {
		return nil, err
	}
	s.changed(ctx, d, true)
	return s.withParticipants(d), nil
}

func (s *Service) Discard(ctx context.Context, id, projectID string, in DocumentCommand) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	digest, err := commandDigest("discard", in)
	if err != nil {
		return nil, err
	}
	d, replayed, err := s.checkedSnapshotCommand(ctx, id, projectID, in, digest)
	if err != nil || replayed {
		return d, err
	}
	previous := d.Revision
	d.Draft, d.EOL, d.MixedEOL = d.BaseContent, d.BaseEOL, d.BaseMixedEOL
	d.Dirty = false
	// Clearing the draft reference preserves the held edit in version history.
	d.HeldAgentVersionID = ""
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	if err := s.commitSnapshotCommand(ctx, d, previous, actorRestore, in, digest); err != nil {
		return nil, err
	}
	s.changed(ctx, d, true)
	return s.withParticipants(d), nil
}

func (s *Service) Reload(ctx context.Context, p *project.Project, id string, in DocumentCommand) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	digest, err := commandDigest("reload", in)
	if err != nil {
		return nil, err
	}
	d, replayed, err := s.checkedSnapshotCommand(ctx, id, p.ID, in, digest)
	if err != nil || replayed {
		return d, err
	}
	read, err := readDocumentSource(p, d)
	if err != nil {
		return nil, err
	}
	if err := validateEditableSource(read); err != nil {
		return nil, err
	}
	content, eol, mixed := normalizeEOL(read.Content)
	previous := d.Revision
	d.Draft, d.BaseContent, d.BaseSHA256, d.Encoding = content, content, read.SHA256, read.Encoding
	d.SizeBytes = read.SizeBytes
	d.EOL, d.BaseEOL, d.MixedEOL, d.BaseMixedEOL = eol, eol, mixed, mixed
	d.Dirty, d.Diverged = false, false
	// The file replaces the draft; the held edit stays in version history.
	d.HeldAgentVersionID = ""
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	if err := s.commitSnapshotCommand(ctx, d, previous, actorExternal, in, digest); err != nil {
		return nil, err
	}
	s.changed(ctx, d, true)
	return s.withParticipants(d), nil
}
