package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// PromotedDocumentSync carries promotion authorship and path for a landed file.
type PromotedDocumentSync struct {
	RootID     string
	Path       string
	SessionID  string
	JobID      string
	Turn       int
	ToolCallID string
	ToolName   string
	Deleted    bool
}

// PromotedDocumentResult captures pre- and post-promotion text identities for ledger recording.
type PromotedDocumentResult struct {
	DocumentID string
	TextBefore *sourceledger.TextState
	TextAfter  *sourceledger.TextState
}

// PromotedDocumentHold keeps open editor documents consistent with one overlay
// promotion: their imports become durable in the promotion transaction or not at all.
type PromotedDocumentHold interface {
	// Stage imports landed bytes into the held documents' live replicas without
	// persisting them. Documents that cannot be staged are left to external observation.
	Stage(ctx context.Context, syncs []PromotedDocumentSync) map[PathRef]PromotedDocumentResult
	// CommitTx writes the staged imports inside the promotion transaction.
	CommitTx(ctx context.Context, tx *sql.Tx) error
	// Release publishes committed imports, discards the rest, and re-observes
	// held paths that were not imported. Only the first call has an effect.
	Release(ctx context.Context, committed bool)
}

type heldPromotedDocument struct {
	ref     PathRef
	id      string
	release func()
}

type stagedPromotedDocument struct {
	ref      PathRef
	previous int64
	replica  *stagedReplica
}

type promotedDocumentHold struct {
	service    *Service
	project    *project.Project
	held       []heldPromotedDocument
	staged     []stagedPromotedDocument
	unlockOps  func()
	unlockDocs func()
	written    bool
	released   bool
}

// HoldPromotedDocuments reserves the sources of open documents at promotion
// paths before any bytes land, so external observation cannot import the
// promoted bytes without their promotion attribution.
func (s *Service) HoldPromotedDocuments(ctx context.Context, p *project.Project, refs []PathRef) (PromotedDocumentHold, error) {
	hold := &promotedDocumentHold{service: s, project: p}
	if s == nil || s.store == nil || p == nil {
		return hold, nil
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	seen := make(map[PathRef]struct{}, len(refs))
	for _, ref := range refs {
		ref = PathRef{RootID: strings.TrimSpace(ref.RootID), Path: strings.TrimSpace(ref.Path)}
		if _, dup := seen[ref]; dup || ref.RootID == "" || ref.Path == "" {
			continue
		}
		seen[ref] = struct{}{}
		d, err := s.store.GetByIdentity(ctx, p.ID, p.BranchForRoot(ref.RootID), ref.RootID, ref.Path)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			hold.releaseSources()
			return nil, err
		}
		release, err := s.reserveDocumentSource(ctx, p, d)
		// Another file operation owns the path; observation settles the document after it.
		if errors.Is(err, projectsource.ErrSourceBusy) {
			continue
		}
		if err != nil {
			hold.releaseSources()
			return nil, err
		}
		hold.held = append(hold.held, heldPromotedDocument{ref: ref, id: d.ID, release: release})
	}
	return hold, nil
}

func (h *promotedDocumentHold) Stage(ctx context.Context, syncs []PromotedDocumentSync) map[PathRef]PromotedDocumentResult {
	if h == nil || h.released || len(h.held) == 0 || h.unlockDocs != nil {
		return nil
	}
	s := h.service
	byRef := make(map[PathRef]string, len(h.held))
	for _, held := range h.held {
		byRef[held.ref] = held.id
	}
	type target struct {
		id  string
		req PromotedDocumentSync
	}
	targets := make([]target, 0, len(syncs))
	ids := make([]string, 0, len(syncs))
	for _, req := range syncs {
		id, ok := byRef[PathRef{RootID: strings.TrimSpace(req.RootID), Path: strings.TrimSpace(req.Path)}]
		if !ok || req.Deleted {
			continue
		}
		targets = append(targets, target{id: id, req: req})
		ids = append(ids, id)
	}
	if len(targets) == 0 {
		return nil
	}
	sort.Strings(ids)
	s.ops.RLock()
	h.unlockOps = s.ops.RUnlock
	h.unlockDocs = s.lockDocuments(ids)
	results := make(map[PathRef]PromotedDocumentResult, len(targets))
	for _, t := range targets {
		staged, result, err := s.stagePromotedDocument(ctx, h.project, t.id, t.req)
		if err != nil {
			slog.WarnContext(ctx, "stage promoted document failed", "path", t.req.Path, "err", err)
			continue
		}
		if staged == nil {
			continue
		}
		h.staged = append(h.staged, *staged)
		results[staged.ref] = result
	}
	return results
}

func (h *promotedDocumentHold) CommitTx(ctx context.Context, tx *sql.Tx) error {
	if h == nil || h.released || len(h.staged) == 0 {
		return nil
	}
	for i := range h.staged {
		staged := &h.staged[i]
		if err := h.service.store.UpdateCASTx(ctx, tx, &staged.replica.next, staged.previous); err != nil {
			return err
		}
	}
	h.written = true
	return nil
}

func (h *promotedDocumentHold) Release(ctx context.Context, committed bool) {
	if h == nil || h.released {
		return
	}
	h.released = true
	s := h.service
	publish := committed && h.written
	imported := make(map[PathRef]struct{}, len(h.staged))
	for i := range h.staged {
		staged := &h.staged[i]
		s.settleStagedReplica(ctx, staged.replica, publish)
		if publish {
			imported[staged.ref] = struct{}{}
			s.changed(ctx, &staged.replica.next, true)
		}
	}
	if h.unlockDocs != nil {
		h.unlockDocs()
	}
	if h.unlockOps != nil {
		h.unlockOps()
	}
	h.releaseSources()
	var pending []PathRef
	for _, held := range h.held {
		if _, ok := imported[held.ref]; !ok {
			pending = append(pending, held.ref)
		}
	}
	if len(pending) == 0 {
		return
	}
	// Observation skipped these paths while they were reserved.
	if err := s.ObserveExternal(context.WithoutCancel(ctx), h.project, pending); err != nil {
		slog.WarnContext(ctx, "observe promoted documents failed", "err", err)
	}
}

func (h *promotedDocumentHold) releaseSources() {
	for i := len(h.held) - 1; i >= 0; i-- {
		if release := h.held[i].release; release != nil {
			release()
			h.held[i].release = nil
		}
	}
}

// stagePromotedDocument prepares one held document's import under its document lock.
func (s *Service) stagePromotedDocument(ctx context.Context, p *project.Project, id string, req PromotedDocumentSync) (*stagedPromotedDocument, PromotedDocumentResult, error) {
	d, err := s.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, PromotedDocumentResult{}, nil
	}
	if err != nil {
		return nil, PromotedDocumentResult{}, err
	}
	ref := PathRef{RootID: d.RootID, Path: d.Path}
	if ref.RootID != strings.TrimSpace(req.RootID) || ref.Path != strings.TrimSpace(req.Path) {
		return nil, PromotedDocumentResult{}, nil
	}
	read, err := readDocumentSource(p, d)
	if err != nil {
		var unsupported *projectsource.SourceUnsupportedEncodingError
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, projectsource.ErrSourceNotFound) || errors.As(err, &unsupported) {
			return nil, PromotedDocumentResult{}, nil
		}
		return nil, PromotedDocumentResult{}, err
	}
	if read.Binary || read.OverLimit || read.Encoding == "" || d.BaseSHA256 == read.SHA256 {
		return nil, PromotedDocumentResult{}, nil
	}

	result := PromotedDocumentResult{DocumentID: d.ID}
	if checkpoint, err := s.publicationCheckpoint(ctx, d); err == nil {
		result.TextBefore, _ = s.publicationTextState(ctx, d, checkpoint)
		if result.TextBefore != nil {
			result.TextBefore.Revision = d.Revision
		}
	}
	toolName := strings.TrimSpace(req.ToolName)
	if toolName == "" {
		toolName = "promote_overlay"
	}
	d.authorship = authorshipContext{
		sessionID:  strings.TrimSpace(req.SessionID),
		jobID:      strings.TrimSpace(req.JobID),
		turn:       req.Turn,
		toolCallID: strings.TrimSpace(req.ToolCallID),
		toolName:   toolName,
	}
	previous := d.Revision
	replica, err := s.stagePublishedReplica(ctx, d, read, textActor{kind: actorAgent}, nil)
	if err != nil {
		return nil, PromotedDocumentResult{}, err
	}
	// The replica entry still carries the committed revision, so text state reads use the pre-image document.
	if checkpoint, err := s.stagedCheckpoint(ctx, replica); err == nil {
		result.TextAfter, _ = s.publicationTextState(ctx, d, checkpoint)
		if result.TextAfter != nil {
			result.TextAfter.Revision = replica.next.Revision
		}
	}
	return &stagedPromotedDocument{ref: ref, previous: previous, replica: replica}, result, nil
}

// stagedCheckpoint reads the checkpoint of a staged, not yet durable replica.
func (s *Service) stagedCheckpoint(ctx context.Context, staged *stagedReplica) ([]byte, error) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	if s.replicas.entries[staged.next.ID] != staged.entry {
		return nil, errors.New("staged replica is no longer resident")
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", Handle: staged.entry.handle, Checkpoint: true})
	if err != nil {
		return nil, err
	}
	if snapshot.Text != staged.next.Draft {
		return nil, errors.New("staged text does not match its CRDT checkpoint")
	}
	return snapshot.Checkpoint, nil
}
