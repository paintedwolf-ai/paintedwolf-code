package sourceledger

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// PreparedRecording holds immutable inputs and a retention lease until Close.
// Close must follow the caller's transaction commit or rollback.
type PreparedRecording interface {
	CommitTx(context.Context, *sql.Tx) (TrackedFile, error)
	Close()
}

type preparedRecording struct {
	mu      sync.Mutex
	store   *Store
	inputs  []RecordInput
	release func()
}

// Prepare publishes content before the caller acquires a database writer.
func (s *Store) Prepare(ctx context.Context, inputs []RecordInput) (PreparedRecording, error) {
	if s == nil || s.sqlDB == nil {
		return nil, errors.New("ledger not configured")
	}
	if len(inputs) > 0 {
		if err := validateBatch(inputs); err != nil {
			return nil, err
		}
	}
	p := &preparedRecording{store: s, release: s.objects.AcquireReferenceLease()}
	for _, raw := range inputs {
		in := normalizedInput(raw)
		in.Before, in.After = bytes.Clone(in.Before), bytes.Clone(in.After)
		in.TextBefore, in.TextAfter = cloneTextState(in.TextBefore), cloneTextState(in.TextAfter)
		in.objects = make(map[string]*db.UpsertSourceBlobObjectParams, 2)
		for _, content := range []struct {
			sha   string
			bytes []byte
		}{{in.BeforeSHA256, in.Before}, {in.AfterSHA256, in.After}} {
			object, err := prepareContent(ctx, s.objects, content.sha, content.bytes, in.EntryKind)
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
	rel, stored, oids, err := objects.Put(sha, content)
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
