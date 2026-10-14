package editordoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
)

type saveReplay struct {
	document *Document
	mutation *Mutation
}

// findSaveReplay validates reused input; nil means the operation ID is unused.
func (c savePublisher) findSaveReplay(ctx context.Context, p *project.Project, id, operationID string, actor saveActor, expected int64) (*saveReplay, error) {
	s := c.s
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

func (c savePublisher) resumeSave(ctx context.Context, p *project.Project, replay *saveReplay) (*Document, error) {
	s := c.s
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

// Compact receipts prevent duplicate publication on reconnect.
func (c savePublisher) mutationResponse(ctx context.Context, current *Document, m *Mutation) (*Document, error) {
	s := c.s
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

// settleRejectedReservation ends a save that preflight definitely rejected,
// recording the rejection before releasing its reserved bytes. Uncertain storage
// failures leave the reservation intact for automatic replay.
func (c savePublisher) settleRejectedReservation(ctx context.Context, d *Document, operationID string, actor saveActor, cause error) error {
	s := c.s
	if !savePreflightRejected(cause) {
		return nil
	}
	if _, err := s.store.Mutation(ctx, operationID); err == nil {
		return nil // Publication already transferred the reservation to its journal.
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("read save rejection journal: %w", err)
	}
	now := time.Now().UTC()
	mutation := &Mutation{ID: operationID, DocumentID: d.ID, ProjectID: d.ProjectID, BranchID: d.BranchID,
		FileID: d.FileID, RootID: d.RootID, Path: d.Path, ClientID: actor.ClientID,
		InputDigest: saveInputDigest(d.ID, d.ProjectID, actor, d.Revision), DraftRevision: d.Revision,
		ExpectedSHA256: d.BaseSHA256, Encoding: d.Encoding, EOL: d.EOL,
		Status: "failed", Error: cause.Error(), SessionID: actor.SessionID, Turn: actor.Turn,
		Origin: actor.Origin, PersonID: actor.PersonID, CreatedAt: now, UpdatedAt: now}
	if err := s.store.InsertMutation(ctx, mutation); err != nil {
		return fmt.Errorf("preserve save rejection: %w", err)
	}
	return nil
}

func savePreflightRejected(err error) bool {
	var rejected *documentcore.Rejected
	return errors.As(err, &rejected) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrRevisionConflict) || errors.Is(err, ErrRootDetached) ||
		errors.Is(err, projectsource.ErrSourceNotFound) || errors.Is(err, projectsource.ErrSourcePathDenied) || errors.Is(err, projectsource.ErrSourceWriteTooLarge) ||
		errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) || errors.Is(err, textfile.ErrBinary) || errors.Is(err, textfile.ErrUnsupported)
}
