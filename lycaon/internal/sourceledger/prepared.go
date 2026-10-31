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

// BatchPreparedRecording extends PreparedRecording with a transaction-scoped cache.
type BatchPreparedRecording interface {
	PreparedRecording
	CommitTxWithCache(context.Context, *sql.Tx, *TxBatchCache) (TrackedFile, error)
}

type dirCacheKey struct {
	project, branch, root, rel string
}

type dirEntryKey struct {
	dirID, name string
}

// TxBatchCache caches directory lookups, blob upserts, and ordinal increments
// within one caller-owned SQLite write transaction.
type TxBatchCache struct {
	tx                *sql.Tx
	upsertedBlobs     map[string]bool
	directories       map[dirCacheKey]db.SourceDirectories
	hasChildSubdirs   map[string]bool
	dirHasActiveHeads map[string]bool
	loadedDirs        map[string]bool
	seenDirEntries    map[dirEntryKey]bool
	freshOperations   bool
	currentOrdinal    map[string]int64
	dirtyOrdinal      map[string]bool

	operations    []db.InsertSourceOperationParams
	files         []db.InsertSourceFileParams
	versions      []db.InsertSourceVersionParams
	effects       []db.InsertSourceEffectParams
	heads         []db.UpsertSourceHeadEntryParams
	presentations []db.InsertSourceAgentPresentationParams
	lineAttrs     []db.InsertSourceLineAttrParams
}

// NewTxBatchCache constructs a transaction-scoped cache for batch settlements.
func NewTxBatchCache(freshOperations bool) *TxBatchCache {
	return &TxBatchCache{
		upsertedBlobs:     make(map[string]bool),
		directories:       make(map[dirCacheKey]db.SourceDirectories),
		hasChildSubdirs:   make(map[string]bool),
		dirHasActiveHeads: make(map[string]bool),
		loadedDirs:        make(map[string]bool),
		seenDirEntries:    make(map[dirEntryKey]bool),
		freshOperations:   freshOperations,
		currentOrdinal:    make(map[string]int64),
		dirtyOrdinal:      make(map[string]bool),
	}
}

// BindTx attaches the active SQLite write transaction to the batch cache.
func (c *TxBatchCache) BindTx(tx *sql.Tx) {
	if c != nil {
		c.tx = tx
	}
}

func (c *TxBatchCache) directoryHasChildSubdirs(ctx context.Context, dirID string) (bool, error) {
	if has, known := c.hasChildSubdirs[dirID]; known {
		return has, nil
	}
	if c.tx == nil {
		return true, nil
	}
	var exists int64
	if err := c.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_directories WHERE parent_id = ? LIMIT 1)`, dirID).Scan(&exists); err != nil {
		return true, err
	}
	has := exists != 0
	c.hasChildSubdirs[dirID] = has
	return has, nil
}

func (c *TxBatchCache) directoryHasActiveHeads(ctx context.Context, dirID string) (bool, error) {
	if has, known := c.dirHasActiveHeads[dirID]; known {
		return has, nil
	}
	if c.tx == nil {
		return true, nil
	}
	var exists int64
	if err := c.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_head_entries WHERE directory_id = ? AND state != 'absent' LIMIT 1)`, dirID).Scan(&exists); err != nil {
		return true, err
	}
	has := exists != 0
	c.dirHasActiveHeads[dirID] = has
	return has, nil
}

func (c *TxBatchCache) ensureDirectoryEntriesLoaded(ctx context.Context, dirID string) error {
	if c == nil || c.tx == nil || c.loadedDirs[dirID] {
		return nil
	}
	hasSubdirs, err := c.directoryHasChildSubdirs(ctx, dirID)
	if err != nil {
		return err
	}
	if hasSubdirs {
		rows, err := c.tx.QueryContext(ctx, `SELECT name FROM source_directories WHERE parent_id = ? AND present = 1`, dirID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			c.seenDirEntries[dirEntryKey{dirID: dirID, name: name}] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_ = rows.Close()
	}
	hasHeads, err := c.directoryHasActiveHeads(ctx, dirID)
	if err != nil {
		return err
	}
	if hasHeads {
		rows, err := c.tx.QueryContext(ctx, `SELECT name FROM source_head_entries WHERE directory_id = ? AND state != 'absent'`, dirID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			c.seenDirEntries[dirEntryKey{dirID: dirID, name: name}] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_ = rows.Close()
	}
	c.loadedDirs[dirID] = true
	return nil
}

func (c *TxBatchCache) advanceOrdinal(ctx context.Context, q *db.Queries, projectID string) (int64, error) {
	if c == nil {
		return q.AdvanceSourceOrdinal(ctx, projectID)
	}
	if ord, ok := c.currentOrdinal[projectID]; ok {
		ord++
		c.currentOrdinal[projectID] = ord
		c.dirtyOrdinal[projectID] = true
		return ord, nil
	}
	ord, err := q.AdvanceSourceOrdinal(ctx, projectID)
	if err != nil {
		return 0, err
	}
	c.currentOrdinal[projectID] = ord
	return ord, nil
}

func execChunkedInsert(ctx context.Context, tx *sql.Tx, prefix, rowPlaceholder, suffix string, colsPerRow, chunk, total int, fillRow func(i int, args []any) []any) error {
	var fullStmt *sql.Stmt
	if total >= chunk*2 {
		var sb strings.Builder
		sb.WriteString(prefix)
		for i := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(rowPlaceholder)
		}
		sb.WriteString(suffix)
		if stmt, err := tx.PrepareContext(ctx, sb.String()); err == nil {
			fullStmt = stmt
			defer func() { _ = fullStmt.Close() }()
		}
	}
	args := make([]any, 0, chunk*colsPerRow)
	for start := 0; start < total; start += chunk {
		end := min(start+chunk, total)
		args = args[:0]
		for i := start; i < end; i++ {
			args = fillRow(i, args)
		}
		if end-start == chunk && fullStmt != nil {
			if _, err := fullStmt.ExecContext(ctx, args...); err != nil {
				return err
			}
			continue
		}
		var sb strings.Builder
		sb.WriteString(prefix)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString(rowPlaceholder)
		}
		sb.WriteString(suffix)
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func (c *TxBatchCache) flushBufferedInserts(ctx context.Context, tx *sql.Tx) error {
	if c == nil || tx == nil {
		return nil
	}
	if len(c.operations) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_operations (id, project_id, branch_id, origin, person_id, cause, actor_label, session_id, job_id, turn, tool_call_id, tool_name, batch_id, operation_key, capture_quality, started_ts, committed_ts, git_transition_id, command_window_id) VALUES `,
			`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ``, 19, 128, len(c.operations),
			func(i int, args []any) []any {
				op := c.operations[i]
				return append(args, op.ID, op.ProjectID, op.BranchID, op.Origin, op.PersonID, op.Cause, op.ActorLabel, op.SessionID, op.JobID, op.Turn, op.ToolCallID, op.ToolName, op.BatchID, op.OperationKey, op.CaptureQuality, op.StartedTs, op.CommittedTs, op.GitTransitionID, op.CommandWindowID)
			}); err != nil {
			return err
		}
		c.operations = c.operations[:0]
	}
	if len(c.files) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES `,
			`(?,?,?,?)`, ``, 4, 256, len(c.files),
			func(i int, args []any) []any {
				f := c.files[i]
				return append(args, f.ID, f.ProjectID, f.EntryKind, f.CreatedTs)
			}); err != nil {
			return err
		}
		c.files = c.files[:0]
	}
	if len(c.versions) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_versions (id, file_id, project_id, branch_id, parent_version_id, derived_from_version_id, operation_id, root_id, path, state, content_sha256, byte_size, capture_state, capture_reason, capture_quality, created_ts, seq, landing) VALUES `,
			`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ``, 18, 128, len(c.versions),
			func(i int, args []any) []any {
				v := c.versions[i]
				return append(args, v.ID, v.FileID, v.ProjectID, v.BranchID, v.ParentVersionID, v.DerivedFromVersionID, v.OperationID, v.RootID, v.Path, v.State, v.ContentSha256, v.ByteSize, v.CaptureState, v.CaptureReason, v.CaptureQuality, v.CreatedTs, v.Seq, v.Landing)
			}); err != nil {
			return err
		}
		c.versions = c.versions[:0]
	}
	if len(c.effects) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_effects (id, project_id, operation_id, file_id, before_version_id, after_version_id, root_id, path, from_root_id, from_path, op, entry_kind, ordinal, walk_visible, created_ts) VALUES `,
			`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ``, 15, 128, len(c.effects),
			func(i int, args []any) []any {
				e := c.effects[i]
				return append(args, e.ID, e.ProjectID, e.OperationID, e.FileID, e.BeforeVersionID, e.AfterVersionID, e.RootID, e.Path, e.FromRootID, e.FromPath, e.Op, e.EntryKind, e.Ordinal, e.WalkVisible, e.CreatedTs)
			}); err != nil {
			return err
		}
		c.effects = c.effects[:0]
	}
	if len(c.heads) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_head_entries (project_id, branch_id, file_id, version_id, root_id, directory_id, name, state, content_sha256, ordinal, observed_ts) VALUES `,
			`(?,?,?,?,?,?,?,?,?,?,?)`,
			` ON CONFLICT(project_id, branch_id, file_id) DO UPDATE SET version_id = excluded.version_id, root_id = excluded.root_id, directory_id = excluded.directory_id, name = excluded.name, state = excluded.state, content_sha256 = excluded.content_sha256, ordinal = excluded.ordinal, observed_ts = excluded.observed_ts`,
			11, 128, len(c.heads),
			func(i int, args []any) []any {
				h := c.heads[i]
				return append(args, h.ProjectID, h.BranchID, h.FileID, h.VersionID, h.RootID, h.DirectoryID, h.Name, h.State, h.ContentSha256, h.Ordinal, h.ObservedTs)
			}); err != nil {
			return err
		}
		c.heads = c.heads[:0]
	}
	if len(c.presentations) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_agent_presentations (effect_id, project_id, file_id, ordinal) VALUES `,
			`(?,?,?,?)`, ``, 4, 256, len(c.presentations),
			func(i int, args []any) []any {
				p := c.presentations[i]
				return append(args, p.EffectID, p.ProjectID, p.FileID, p.Ordinal)
			}); err != nil {
			return err
		}
		c.presentations = c.presentations[:0]
	}
	if len(c.lineAttrs) > 0 {
		if err := execChunkedInsert(ctx, tx,
			`INSERT INTO source_line_attr (project_id, branch_id, file_id, start_line, end_line, effect_id) VALUES `,
			`(?,?,?,?,?,?)`, ``, 6, 256, len(c.lineAttrs),
			func(i int, args []any) []any {
				l := c.lineAttrs[i]
				return append(args, l.ProjectID, l.BranchID, l.FileID, l.StartLine, l.EndLine, l.EffectID)
			}); err != nil {
			return err
		}
		c.lineAttrs = c.lineAttrs[:0]
	}
	return nil
}

// Flush writes any buffered inserts and project ordinal increments to tx before commit.
func (c *TxBatchCache) Flush(ctx context.Context, tx *sql.Tx) error {
	if c == nil {
		return nil
	}
	if err := c.flushBufferedInserts(ctx, tx); err != nil {
		return err
	}
	for projectID, dirty := range c.dirtyOrdinal {
		if !dirty {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET source_history_ordinal = ? WHERE id = ?`, c.currentOrdinal[projectID], projectID); err != nil {
			return err
		}
		c.dirtyOrdinal[projectID] = false
	}
	return nil
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
	return s.prepareWithCache(ctx, inputs, nil)
}

// PrepareMany publishes content for multiple batches while deduplicating
// identical blob payloads across the batch set.
func (s *Store) PrepareMany(ctx context.Context, batches [][]RecordInput) ([]PreparedRecording, error) {
	if s == nil || s.sqlDB == nil {
		return nil, errors.New("ledger not configured")
	}
	out := make([]PreparedRecording, 0, len(batches))
	sharedObjects := make(map[string]*db.UpsertSourceBlobObjectParams)
	for _, inputs := range batches {
		rec, err := s.prepareWithCache(ctx, inputs, sharedObjects)
		if err != nil {
			for _, item := range out {
				item.Close()
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *Store) prepare(ctx context.Context, inputs []RecordInput) (*preparedRecording, error) {
	return s.prepareWithCache(ctx, inputs, nil)
}

func (s *Store) prepareWithCache(ctx context.Context, inputs []RecordInput, sharedObjects map[string]*db.UpsertSourceBlobObjectParams) (*preparedRecording, error) {
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
			if sharedObjects != nil && content.sha != "" && content.bytes != nil && in.EntryKind != EntryKindDirectory && len(content.bytes) <= MaxRevisionContentBytes {
				if cached, ok := sharedObjects[content.sha]; ok && cached.Size == int64(len(content.bytes)) && sourceblob.ContentSHA(content.bytes) == strings.ToLower(strings.TrimSpace(content.sha)) {
					in.objects[content.sha] = cached
					continue
				}
			}
			object, err := prepareContent(ctx, s.Content, content.sha, content.bytes, in.EntryKind)
			if err != nil {
				p.Close()
				return nil, err
			}
			if object != nil {
				in.objects[content.sha] = object
				if sharedObjects != nil {
					sharedObjects[content.sha] = object
				}
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
	return p.CommitTxWithCache(ctx, tx, nil)
}

func (p *preparedRecording) CommitTxWithCache(ctx context.Context, tx *sql.Tx, cache *TxBatchCache) (TrackedFile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release == nil || tx == nil {
		return TrackedFile{}, errors.New("prepared history is closed or transaction is missing")
	}
	if len(p.inputs) == 0 {
		return TrackedFile{}, nil
	}
	return p.store.recordBatchIdentityTxWithCache(ctx, p.store.queries.WithTx(tx), p.inputs, cache)
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
