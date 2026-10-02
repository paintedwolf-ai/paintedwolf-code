package authzcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/spawn"
)

// SQLStore persists authorization_contexts rows with per-session hash chains.
type SQLStore struct {
	db db.Handle
}

// NewSQLStore wraps a SQLite handle that already has schema.sql applied.
func NewSQLStore(sqlDB db.Handle) *SQLStore {
	return &SQLStore{db: sqlDB}
}

// AppendContext appends a chained row when config_hash drifted from the caller's perspective.
func (s *SQLStore) AppendContext(ctx context.Context, c Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrNilStore
	}
	sessionID := strings.TrimSpace(c.SessionID)
	if sessionID == "" {
		return false, fmt.Errorf("authzcontext: empty session_id")
	}
	if strings.TrimSpace(c.ID) == "" {
		return false, fmt.Errorf("authzcontext: empty id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var prevSeq int
	var prevHash, latestConfig string
	err = tx.QueryRowContext(ctx, `
		SELECT context_seq, row_hash, config_hash
		FROM authorization_contexts
		WHERE session_id = ?
		ORDER BY context_seq DESC
		LIMIT 1
	`, sessionID).Scan(&prevSeq, &prevHash, &latestConfig)
	if err != nil && !db.IsNoRows(err) {
		return false, fmt.Errorf("authzcontext latest: %w", err)
	}
	if err == nil && latestConfig == c.ConfigHash {
		return false, nil
	}
	seq := 1
	if err == nil {
		seq = prevSeq + 1
	} else {
		prevHash = ""
	}
	sealed := c.SealedAt
	if sealed.IsZero() {
		sealed = time.Now().UTC()
	}
	ts := db.FormatTime(sealed)
	hashVersion := c.HashVersion
	if hashVersion == 0 {
		hashVersion = HashVersion1
	}
	payload := chainPayloadFromContext(c)
	rowHash, err := ComputeRowHash(hashVersion, sessionID, seq, ts, payload, prevHash)
	if err != nil {
		return false, err
	}
	cols, err := marshalContextColumns(c)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO authorization_contexts (
			id, session_id, context_seq, prev_hash, row_hash, hash_version, ts,
			worker_job_id, parent_session_id, project_id, agent_type,
			tool_profile, posture, approval_posture,
			allowed_tools_json, deny_tools_json, mcp_deny_json,
			read_globs_json, write_globs_json, ask_rules_json,
			grants_json, chat_grants_json, never_ask,
			max_tool_loops, mcp_inventory_json, spawn_allowlist_json, worker_tool_budget_json,
			config_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.ID, sessionID, seq, prevHash, rowHash, hashVersion, ts,
		c.WorkerJobID, c.ParentSessionID, c.ProjectID, c.AgentType,
		c.ToolProfile, c.Posture, string(c.ApprovalPosture),
		cols.allowed, cols.deny, cols.mcpDeny,
		cols.readGlobs, cols.writeGlobs, cols.rules,
		cols.grants, cols.chatGrants, c.NeverAsk,
		c.MaxToolLoops, cols.mcpInventory, cols.spawnAllowlist, cols.workerBudget,
		c.ConfigHash,
	)
	if err != nil {
		return false, fmt.Errorf("authzcontext insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLStore) LatestContext(ctx context.Context, sessionID string) (*Context, error) {
	rows, err := s.ListContexts(ctx, sessionID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	last := rows[len(rows)-1]
	return &last, nil
}

// LatestContextTx resolves the newest context row through the caller's
// transaction, so in-tx seals never read the pool while holding the write lock.
func (s *SQLStore) LatestContextTx(ctx context.Context, tx *sql.Tx, sessionID string) (*Context, error) {
	if s == nil || tx == nil {
		return nil, ErrNilStore
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, context_seq, prev_hash, row_hash, hash_version, ts,
			worker_job_id, parent_session_id, project_id, agent_type,
			tool_profile, posture, approval_posture,
			allowed_tools_json, deny_tools_json, mcp_deny_json,
			read_globs_json, write_globs_json, ask_rules_json,
			grants_json, chat_grants_json, never_ask,
			max_tool_loops, mcp_inventory_json, spawn_allowlist_json, worker_tool_budget_json,
			config_hash
		FROM authorization_contexts
		WHERE session_id = ?
		ORDER BY context_seq DESC
		LIMIT 1
	`, sessionID)
	c, err := scanContextRow(row)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("authzcontext latest (tx): %w", err)
	}
	return &c, nil
}

func (s *SQLStore) ListContexts(ctx context.Context, sessionID string) ([]Context, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	qrows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, context_seq, prev_hash, row_hash, hash_version, ts,
			worker_job_id, parent_session_id, project_id, agent_type,
			tool_profile, posture, approval_posture,
			allowed_tools_json, deny_tools_json, mcp_deny_json,
			read_globs_json, write_globs_json, ask_rules_json,
			grants_json, chat_grants_json, never_ask,
			max_tool_loops, mcp_inventory_json, spawn_allowlist_json, worker_tool_budget_json,
			config_hash
		FROM authorization_contexts
		WHERE session_id = ?
		ORDER BY context_seq ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("authzcontext list: %w", err)
	}
	defer func() { _ = qrows.Close() }()
	var out []Context
	for qrows.Next() {
		c, err := scanContextRow(qrows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, qrows.Err()
}

type contextColumns struct {
	allowed, deny, mcpDeny, readGlobs, writeGlobs, rules, grants, chatGrants string
	mcpInventory, spawnAllowlist, workerBudget                               string
}

func marshalContextColumns(c Context) (contextColumns, error) {
	var cols contextColumns
	var err error
	if cols.allowed, err = marshalJSON(c.AllowedTools); err != nil {
		return cols, err
	}
	if cols.deny, err = marshalJSON(c.DenyTools); err != nil {
		return cols, err
	}
	if cols.mcpDeny, err = marshalJSON(c.MCPDeny); err != nil {
		return cols, err
	}
	if cols.readGlobs, err = marshalJSON(c.ReadGlobs); err != nil {
		return cols, err
	}
	if cols.writeGlobs, err = marshalJSON(c.WriteGlobs); err != nil {
		return cols, err
	}
	if cols.rules, err = marshalJSON(c.AskRules); err != nil {
		return cols, err
	}
	if cols.grants, err = marshalJSON(c.Grants); err != nil {
		return cols, err
	}
	if cols.chatGrants, err = marshalJSON(c.ChatGrants); err != nil {
		return cols, err
	}
	if cols.mcpInventory, err = marshalJSON(c.MCPInventory); err != nil {
		return cols, err
	}
	if cols.spawnAllowlist, err = marshalJSON(c.SpawnAllowlist); err != nil {
		return cols, err
	}
	if cols.workerBudget, err = marshalJSON(spawnToolBudgetToWire(c.WorkerToolBudget)); err != nil {
		return cols, err
	}
	return cols, nil
}

type contextScanner interface {
	Scan(dest ...any) error
}

func scanContextRow(row contextScanner) (Context, error) {
	var c Context
	var posture string
	var allowed, deny, mcpDeny, readGlobs, writeGlobs, rules, grants, chatGrants string
	var mcpInventory, spawnAllowlist, workerBudget, ts string
	err := row.Scan(
		&c.ID, &c.SessionID, &c.ContextSeq, &c.PrevHash, &c.RowHash, &c.HashVersion, &ts,
		&c.WorkerJobID, &c.ParentSessionID, &c.ProjectID, &c.AgentType,
		&c.ToolProfile, &c.Posture, &posture,
		&allowed, &deny, &mcpDeny,
		&readGlobs, &writeGlobs, &rules,
		&grants, &chatGrants, &c.NeverAsk,
		&c.MaxToolLoops, &mcpInventory, &spawnAllowlist, &workerBudget,
		&c.ConfigHash,
	)
	if err != nil {
		return Context{}, err
	}
	// Read verbatim, since row_hash covers the token; an unknown one is refused.
	if err := c.ApprovalPosture.UnmarshalText([]byte(posture)); err != nil {
		return Context{}, fmt.Errorf("authorization context %s: %w", c.ID, err)
	}
	if c.AllowedTools, err = unmarshalStrings(allowed); err != nil {
		return Context{}, err
	}
	if c.DenyTools, err = unmarshalStrings(deny); err != nil {
		return Context{}, err
	}
	if c.MCPDeny, err = unmarshalStrings(mcpDeny); err != nil {
		return Context{}, err
	}
	if c.ReadGlobs, err = unmarshalStrings(readGlobs); err != nil {
		return Context{}, err
	}
	if c.WriteGlobs, err = unmarshalStrings(writeGlobs); err != nil {
		return Context{}, err
	}
	if err := json.Unmarshal([]byte(nullJSONArray(rules)), &c.AskRules); err != nil {
		return Context{}, fmt.Errorf("authzcontext ask_rules: %w", err)
	}
	if err := json.Unmarshal([]byte(nullJSONArray(grants)), &c.Grants); err != nil {
		return Context{}, fmt.Errorf("authzcontext grants: %w", err)
	}
	if err := json.Unmarshal([]byte(nullJSONArray(chatGrants)), &c.ChatGrants); err != nil {
		return Context{}, fmt.Errorf("authzcontext task grants: %w", err)
	}
	if err := json.Unmarshal([]byte(nullJSONArray(mcpInventory)), &c.MCPInventory); err != nil {
		return Context{}, fmt.Errorf("authzcontext mcp_inventory: %w", err)
	}
	if c.SpawnAllowlist, err = unmarshalStrings(spawnAllowlist); err != nil {
		return Context{}, err
	}
	var budget spawnToolBudgetWire
	if err := json.Unmarshal([]byte(nullJSONArray(workerBudget)), &budget); err != nil {
		return Context{}, fmt.Errorf("authzcontext worker_tool_budget: %w", err)
	}
	c.WorkerToolBudget = spawn.WorkerToolBudget{Default: budget.Default, Min: budget.Min, Max: budget.Max}
	if parsed, err := db.ParseTime(ts); err == nil {
		c.SealedAt = parsed
	}
	return c, nil
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func unmarshalStrings(raw string) ([]string, error) {
	raw = nullJSONArray(raw)
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func nullJSONArray(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]"
	}
	return raw
}

// AppendEvent appends a chained authz_events row.
func (s *SQLStore) AppendEvent(ctx context.Context, e Event) error {
	if s == nil || s.db == nil {
		return ErrNilStore
	}
	sessionID := strings.TrimSpace(e.SessionID)
	if sessionID == "" {
		return fmt.Errorf("authzcontext: empty session_id")
	}
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("authzcontext: empty id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendEventTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendEventTx appends one immutable chain row inside a caller-supplied commit.
func (s *SQLStore) AppendEventTx(ctx context.Context, tx *sql.Tx, e Event) error {
	if s == nil || tx == nil {
		return ErrNilStore
	}
	sessionID := strings.TrimSpace(e.SessionID)
	if sessionID == "" {
		return fmt.Errorf("authzcontext: empty session_id")
	}
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("authzcontext: empty id")
	}
	var prevSeq int
	var prevHash string
	err := tx.QueryRowContext(ctx, `
		SELECT event_seq, row_hash
		FROM authz_events
		WHERE session_id = ?
		ORDER BY event_seq DESC
		LIMIT 1
	`, sessionID).Scan(&prevSeq, &prevHash)
	if err != nil && !db.IsNoRows(err) {
		return fmt.Errorf("authzcontext latest event: %w", err)
	}
	seq := 1
	if err == nil {
		seq = prevSeq + 1
	} else {
		prevHash = ""
	}
	recorded := e.RecordedAt
	if recorded.IsZero() {
		recorded = time.Now().UTC()
	}
	ts := db.FormatTime(recorded)
	hashVersion := e.HashVersion
	if hashVersion == 0 {
		hashVersion = HashVersion1
	}
	payload := eventChainPayloadFromEvent(e)
	rowHash, err := ComputeEventRowHash(hashVersion, sessionID, seq, ts, payload, prevHash)
	if err != nil {
		return err
	}
	detailJSON := e.DetailJSON
	if strings.TrimSpace(detailJSON) == "" {
		detailJSON = "{}"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO authz_events (
			id, session_id, event_seq, prev_hash, row_hash, hash_version, ts,
			context_seq, action, outcome, resolved_by, resolver_person_id, tool_name, reject_code, detail_json, config_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, sessionID, seq, prevHash, rowHash, hashVersion, ts,
		e.ContextSeq, string(e.Action), string(e.Outcome), string(e.ResolvedBy), db.NullString(e.ResolverPersonID),
		e.ToolName, e.RejectCode, detailJSON, e.ConfigHash,
	)
	if err != nil {
		return fmt.Errorf("authzcontext event insert: %w", err)
	}
	return nil
}

func (s *SQLStore) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	qrows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, event_seq, prev_hash, row_hash, hash_version, ts,
			context_seq, action, outcome, resolved_by, COALESCE(resolver_person_id, ''), tool_name, reject_code, detail_json, config_hash
		FROM authz_events
		WHERE session_id = ?
		ORDER BY event_seq ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("authzcontext list events: %w", err)
	}
	defer func() { _ = qrows.Close() }()
	var out []Event
	for qrows.Next() {
		ev, err := scanEventRow(qrows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, qrows.Err()
}

// ListApprovalDecisionsSince returns approval_decision events recorded at or
// after since, across every session, oldest first.
func (s *SQLStore) ListApprovalDecisionsSince(ctx context.Context, since time.Time) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	qrows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, event_seq, prev_hash, row_hash, hash_version, ts,
			context_seq, action, outcome, resolved_by, COALESCE(resolver_person_id, ''), tool_name, reject_code, detail_json, config_hash
		FROM authz_events
		WHERE action = ? AND ts >= ?
		ORDER BY ts ASC, event_seq ASC
	`, string(EventActionApprovalDecision), db.FormatTime(since))
	if err != nil {
		return nil, fmt.Errorf("authzcontext list approval decisions: %w", err)
	}
	defer func() { _ = qrows.Close() }()
	var out []Event
	for qrows.Next() {
		ev, err := scanEventRow(qrows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, qrows.Err()
}

func scanEventRow(row contextScanner) (Event, error) {
	var e Event
	var action, outcome, resolvedBy, ts string
	err := row.Scan(
		&e.ID, &e.SessionID, &e.EventSeq, &e.PrevHash, &e.RowHash, &e.HashVersion, &ts,
		&e.ContextSeq, &action, &outcome, &resolvedBy, &e.ResolverPersonID, &e.ToolName, &e.RejectCode, &e.DetailJSON, &e.ConfigHash,
	)
	if err != nil {
		return Event{}, err
	}
	e.Action = EventAction(action)
	e.Outcome = EventOutcome(outcome)
	e.ResolvedBy = ResolvedBy(resolvedBy)
	if parsed, err := db.ParseTime(ts); err == nil {
		e.RecordedAt = parsed
	}
	return e, nil
}
