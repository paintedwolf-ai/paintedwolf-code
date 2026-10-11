package sourceledger

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/pkg/api"
)

// PreparedRecording holds immutable inputs and a retention lease until Close.
// Close must follow the caller's transaction commit or rollback.
type PreparedRecording interface {
	CommitTx(context.Context, *sql.Tx) (TrackedFile, error)
	Close()
}

type preparedRecord struct {
	RecordInput
	objects map[string]*db.UpsertSourceBlobObjectParams
}

type preparedRecording struct {
	mu      sync.Mutex
	store   *Store
	inputs  []preparedRecord
	release func()
}

// Prepare publishes content before the caller acquires a database writer.
func (s *Store) Prepare(ctx context.Context, inputs []RecordInput) (PreparedRecording, error) {
	return s.prepare(ctx, inputs)
}

func (s *Store) prepare(ctx context.Context, inputs []RecordInput) (*preparedRecording, error) {
	if s == nil || s.sqlDB == nil {
		return nil, errors.New("ledger not configured")
	}
	if len(inputs) > 0 {
		if err := validateBatch(inputs); err != nil {
			return nil, err
		}
	}
	p := &preparedRecording{store: s, release: s.Content.AcquireReferenceLease()}
	for _, raw := range inputs {
		in := preparedRecord{RecordInput: normalizedInput(raw)}
		in.Before, in.After = bytes.Clone(in.Before), bytes.Clone(in.After)
		in.TextBefore, in.TextAfter = cloneTextState(in.TextBefore), cloneTextState(in.TextAfter)
		in.objects = make(map[string]*db.UpsertSourceBlobObjectParams, 2)
		for _, content := range []struct {
			sha   string
			bytes []byte
		}{{in.BeforeSHA256, in.Before}, {in.AfterSHA256, in.After}} {
			object, err := prepareContent(ctx, s.Content, content.sha, content.bytes, in.EntryKind)
			if err != nil {
				p.Close()
				return nil, err
			}
			if object != nil {
				in.objects[content.sha] = object
			}
		}
		p.inputs = append(p.inputs, in)
	}
	return p, nil
}

func cloneTextState(in *TextState) *TextState {
	if in == nil {
		return nil
	}
	out := *in
	out.Spans = append([]TextSpan(nil), in.Spans...)
	return &out
}

func prepareContent(ctx context.Context, objects *sourceblob.Store, sha string, content []byte, kind string) (*db.UpsertSourceBlobObjectParams, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if kind == EntryKindDirectory || content == nil || len(content) > MaxRevisionContentBytes {
		return nil, nil
	}
	rel, stored, oids, err := objects.Put(ctx, sha, content)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &db.UpsertSourceBlobObjectParams{Sha256: sha, Size: int64(len(content)), StoredSize: stored, StorageRelpath: rel, GitOidSha1: oids.SHA1, GitOidSha256: oids.SHA256}, nil
}

func (p *preparedRecording) CommitTx(ctx context.Context, tx *sql.Tx) (TrackedFile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release == nil || tx == nil {
		return TrackedFile{}, errors.New("prepared history is closed or transaction is missing")
	}
	if len(p.inputs) == 0 {
		return TrackedFile{}, nil
	}
	return p.store.recordBatchIdentityTx(ctx, p.store.queries.WithTx(tx), p.inputs)
}

func (p *preparedRecording) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		p.release()
		p.release = nil
		p.inputs = nil
	}
}

type preparedTrack struct {
	TrackInput
	object *db.UpsertSourceBlobObjectParams
}

// commitObserved filters pending mutations against the transaction's admission scope.
func (p *preparedRecording) commitObserved(ctx context.Context, tx *sql.Tx, scope MutationObservationScope) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release == nil || tx == nil {
		return 0, errors.New("prepared history is closed or transaction is missing")
	}
	admitted := make([]preparedRecord, 0, len(p.inputs))
	for _, in := range p.inputs {
		if scope == nil || !scope.Pending(in.BranchID, in.RootID, in.Path) {
			admitted = append(admitted, in)
		}
	}
	if len(admitted) == 0 {
		return 0, nil
	}
	_, err := p.store.recordBatchIdentityTx(ctx, p.store.queries.WithTx(tx), admitted)
	return len(admitted), err
}

func validateBatch(inputs []RecordInput) error {
	first := inputs[0]
	if strings.TrimSpace(first.ProjectID) == "" || strings.TrimSpace(first.RootID) == "" ||
		strings.TrimSpace(first.Path) == "" || first.Op == "" || first.Origin == "" {
		return fmt.Errorf("source history requires project, root, path, operation, and origin")
	}
	for _, in := range inputs[1:] {
		if in.ProjectID != first.ProjectID || in.BranchID != first.BranchID ||
			in.Origin != first.Origin ||
			in.OperationID != first.OperationID || in.GitTransitionID != first.GitTransitionID ||
			in.CommandWindowID != first.CommandWindowID {
			return fmt.Errorf("source operation effects disagree on operation metadata")
		}
	}
	return nil
}

func normalizedInput(in RecordInput) RecordInput {
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}
	if in.Cause == "" {
		switch in.Origin {
		case api.SourceChangeOriginAgent:
			in.Cause = "agent_tool"
		case api.SourceChangeOriginUser:
			in.Cause = "human_edit"
		default:
			in.Cause = "external_observation"
		}
	}
	if in.CaptureQuality == "" {
		in.CaptureQuality = "exact"
		if in.Origin == api.SourceChangeOriginExternal {
			in.CaptureQuality = "observed"
		}
	}
	if in.EntryKind == "" {
		in.EntryKind = EntryKindFile
	}
	if in.BeforeSHA256 == "" && in.Before != nil {
		in.BeforeSHA256 = sourceblob.ContentSHA(in.Before)
	}
	if in.AfterSHA256 == "" && in.After != nil {
		in.AfterSHA256 = sourceblob.ContentSHA(in.After)
	}
	if in.BeforeSize == 0 && in.Before != nil {
		in.BeforeSize = int64(len(in.Before))
	}
	if in.AfterSize == 0 && in.After != nil {
		in.AfterSize = int64(len(in.After))
	}
	return in
}
