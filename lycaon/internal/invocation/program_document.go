package invocation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// MaxReceiptScopes bounds the scopes one receipt document request names.
const MaxReceiptScopes = 64

// ErrReceiptChanged reports a receipt read pinned to a revision the records
// behind it no longer match.
var ErrReceiptChanged = errors.New("receipt document changed")

// ReceiptRequestError names the request parameter a receipt read refused.
type ReceiptRequestError struct {
	Parameter, Reason string
}

func (e *ReceiptRequestError) Error() string { return e.Parameter + " " + e.Reason }

// ReceiptScope is a whole program, or the executions one host activity selects.
type ReceiptScope struct {
	ProgramID, ActivityID string
}

func (s ReceiptScope) String() string {
	if s.ActivityID == "" {
		return s.ProgramID
	}
	return s.ProgramID + ":" + s.ActivityID
}

// ParseReceiptScopes reads `program_id` and `program_id:activity_id` scopes in
// request order.
func ParseReceiptScopes(values []string) ([]ReceiptScope, error) {
	if len(values) == 0 || len(values) > MaxReceiptScopes {
		return nil, &ReceiptRequestError{"scopes", fmt.Sprintf("must name 1 to %d scopes", MaxReceiptScopes)}
	}
	scopes := make([]ReceiptScope, 0, len(values))
	seen := make(map[ReceiptScope]bool, len(values))
	shape := make(map[string]bool, len(values))
	for _, value := range values {
		programPart, activityPart, scoped := strings.Cut(value, ":")
		program, err := uuid.Parse(programPart)
		if err != nil || len(programPart) != 36 {
			return nil, &ReceiptRequestError{"scopes", "must name program ids as UUIDs"}
		}
		scope := ReceiptScope{ProgramID: program.String()}
		if scoped {
			activity, err := uuid.Parse(activityPart)
			if err != nil || len(activityPart) != 36 {
				return nil, &ReceiptRequestError{"scopes", "must name activity ids as UUIDs"}
			}
			scope.ActivityID = activity.String()
		}
		if seen[scope] {
			return nil, &ReceiptRequestError{"scopes", "must not repeat a scope"}
		}
		if whole, ok := shape[scope.ProgramID]; ok && (whole || scope.ActivityID == "") {
			return nil, &ReceiptRequestError{"scopes", "must not name a program both whole and by activity"}
		}
		seen[scope] = true
		shape[scope.ProgramID] = scope.ActivityID == ""
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// receiptPart is one scope of a document with the counts that place its rows.
type receiptPart struct {
	scope      ReceiptScope
	program    api.CommandProgramReceipt
	progress   *api.CommandProgramProgressReceipt
	activity   *api.CommandActivity
	selection  executionSelection
	executions int
	nodes      int
	untaken    int
	first      int
}

func (p receiptPart) rows() int { return 1 + p.executions + p.nodes }

// receiptDocument is one consistent snapshot of the records behind a document.
type receiptDocument struct {
	parts    []receiptPart
	total    int
	settled  bool
	revision string
}

type receiptProgramRow struct {
	Type           string                             `json:"type"`
	Program        api.CommandProgramReceipt          `json:"program"`
	Progress       *api.CommandProgramProgressReceipt `json:"progress,omitempty"`
	Activity       *api.CommandActivity               `json:"activity,omitempty"`
	ExecutionCount int                                `json:"execution_count"`
	NodeCount      int                                `json:"node_count"`
}

type receiptExecutionRow struct {
	Type       string                      `json:"type"`
	ProgramID  string                      `json:"program_id"`
	ActivityID string                      `json:"activity_id,omitempty"`
	Execution  api.CommandExecutionReceipt `json:"execution"`
}

type receiptNodeRow struct {
	Type      string                 `json:"type"`
	ProgramID string                 `json:"program_id"`
	Node      api.CommandProgramNode `json:"node"`
}

// ReadReceiptDocument reads rows [offset, offset+limit) of the JSON Lines
// receipt for scopes. A non-empty revision pins the read to that snapshot.
func (r *SQLRecorder) ReadReceiptDocument(ctx context.Context, sessionID string, scopes []ReceiptScope, revision string, offset, limit int) (api.CommandReceiptDocumentPage, error) {
	out := api.CommandReceiptDocumentPage{Rows: []api.CommandReceiptRow{}}
	tx, err := r.beginProgramRead(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	document, err := loadReceiptDocument(ctx, tx, sessionID, scopes)
	if err != nil {
		return out, err
	}
	if revision != "" && revision != document.revision {
		return out, ErrReceiptChanged
	}
	if offset > document.total {
		return out, &ReceiptRequestError{"offset", "is past the end of the document"}
	}
	end := min(document.total, offset+limit)
	if out.Rows, err = document.read(ctx, tx, offset, end); err != nil {
		return out, err
	}
	out.Revision, out.TotalRows, out.Settled = document.revision, document.total, document.settled
	out.Offset, out.EndOffset, out.Complete = offset, end, end == document.total
	return out, tx.Commit()
}

func loadReceiptDocument(ctx context.Context, tx *sql.Tx, sessionID string, scopes []ReceiptScope) (receiptDocument, error) {
	document := receiptDocument{parts: make([]receiptPart, 0, len(scopes)), settled: true}
	digest := sha256.New()
	for _, scope := range scopes {
		part, err := loadReceiptPart(ctx, tx, sessionID, scope)
		if err != nil {
			return document, err
		}
		part.first = document.total
		document.total += part.rows()
		if part.program.State == "running" || part.program.State == "waiting" {
			document.settled = false
		}
		writeReceiptRevision(digest, part)
		document.parts = append(document.parts, part)
	}
	document.revision = hex.EncodeToString(digest.Sum(nil))
	return document, nil
}

func loadReceiptPart(ctx context.Context, tx *sql.Tx, sessionID string, scope ReceiptScope) (receiptPart, error) {
	part := receiptPart{scope: scope}
	var err error
	if part.program, err = readProgram(ctx, tx, sessionID, scope.ProgramID); err != nil {
		return part, err
	}
	if part.progress, err = readProgramProgress(ctx, tx, scope.ProgramID); err != nil {
		return part, err
	}
	if scope.ActivityID != "" {
		activities, err := readProgramActivities(ctx, tx, part.program)
		if err != nil {
			return part, err
		}
		if part.selection, err = selectActivityExecutions(activities, scope.ActivityID); err != nil {
			return part, err
		}
		for i := range activities {
			if activities[i].ID == scope.ActivityID {
				part.activity = &activities[i]
			}
		}
	}
	if part.selection.ids == "" {
		// Ordinals are assigned densely from 1 and never reused.
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0) FROM invocation_program_executions WHERE program_id=?`, scope.ProgramID).Scan(&part.executions)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions e WHERE e.program_id=? `+activityExecutionFilter, scope.ProgramID, part.selection.ids, part.selection.ids).Scan(&part.executions)
	}
	if err != nil {
		return part, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(SUM(state='untaken'),0) FROM invocation_program_nodes WHERE program_id=?`, scope.ProgramID).Scan(&part.nodes, &part.untaken)
	return part, err
}

// writeReceiptRevision covers every recorded fact a row can show. Trace events
// advance with each child, output, delivery and settlement; the other fields
// cover the writes that change rows without charging the trace.
func writeReceiptRevision(digest hash.Hash, part receiptPart) {
	program := part.program
	status, settled := "", ""
	if program.StatusCode != nil {
		status = strconv.Itoa(*program.StatusCode)
	}
	if program.SettledAt != nil {
		settled = program.SettledAt.UTC().Format(time.RFC3339Nano)
	}
	for _, field := range []string{part.scope.String(), program.State, status, program.StopCode, program.DetailRef, settled,
		strconv.FormatInt(program.ActivityRevision, 10), program.ProgressState, program.ProgressRef, strconv.Itoa(part.untaken)} {
		_, _ = digest.Write([]byte(field))
		_, _ = digest.Write([]byte{0})
	}
}

// read renders rows [from, to), hydrating only the executions and nodes inside it.
func (d receiptDocument) read(ctx context.Context, tx *sql.Tx, from, to int) ([]api.CommandReceiptRow, error) {
	rows := make([]api.CommandReceiptRow, 0, to-from)
	for _, part := range d.parts {
		start, end := max(from, part.first), min(to, part.first+part.rows())
		if start >= end {
			continue
		}
		local := start - part.first
		localEnd := end - part.first
		if local == 0 {
			text, err := receiptLine(receiptProgramRow{Type: "program", Program: part.program, Progress: part.progress, Activity: part.activity, ExecutionCount: part.executions, NodeCount: part.nodes})
			if err != nil {
				return nil, err
			}
			rows = append(rows, api.CommandReceiptRow{Index: start, Text: text})
			local++
		}
		if execStart, execEnd := max(local-1, 0), min(localEnd-1, part.executions); execStart < execEnd {
			lines, err := part.executionLines(ctx, tx, execStart, execEnd)
			if err != nil {
				return nil, err
			}
			for i, text := range lines {
				rows = append(rows, api.CommandReceiptRow{Index: part.first + 1 + execStart + i, Text: text})
			}
		}
		nodeBase := 1 + part.executions
		if nodeStart, nodeEnd := max(local, nodeBase)-nodeBase, localEnd-nodeBase; nodeStart < nodeEnd {
			lines, err := part.nodeLines(ctx, tx, nodeStart, nodeEnd)
			if err != nil {
				return nil, err
			}
			for i, text := range lines {
				rows = append(rows, api.CommandReceiptRow{Index: part.first + nodeBase + nodeStart + i, Text: text})
			}
		}
	}
	return rows, nil
}

func (p receiptPart) executionLines(ctx context.Context, tx *sql.Tx, from, to int) ([]string, error) {
	var executions []api.CommandExecutionReceipt
	var err error
	if p.selection.ids == "" {
		executions, _, err = readExecutions(ctx, tx, p.scope.ProgramID, int64(from), 0, to-from, p.selection)
	} else {
		executions, _, err = readExecutions(ctx, tx, p.scope.ProgramID, 0, from, to-from, p.selection)
	}
	if err != nil {
		return nil, err
	}
	if len(executions) != to-from {
		return nil, fmt.Errorf("receipt executions %d..%d of program %s are unavailable", from, to, p.scope.ProgramID)
	}
	if err := readExecutionOutputs(ctx, tx, p.scope.ProgramID, executions); err != nil {
		return nil, err
	}
	lines := make([]string, len(executions))
	for i := range executions {
		if lines[i], err = receiptLine(receiptExecutionRow{Type: "execution", ProgramID: p.scope.ProgramID, ActivityID: p.scope.ActivityID, Execution: executions[i]}); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

func (p receiptPart) nodeLines(ctx context.Context, tx *sql.Tx, from, to int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,source_unit_id,start_byte,end_byte,line,column_number,state FROM invocation_program_nodes WHERE program_id=? ORDER BY node_id LIMIT ? OFFSET ?`, p.scope.ProgramID, to-from, from)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	lines := make([]string, 0, to-from)
	for rows.Next() {
		var node api.CommandProgramNode
		if err := rows.Scan(&node.ID, &node.SourceUnitID, &node.StartByte, &node.EndByte, &node.Line, &node.Column, &node.State); err != nil {
			return nil, err
		}
		text, err := receiptLine(receiptNodeRow{Type: "node", ProgramID: p.scope.ProgramID, Node: node})
		if err != nil {
			return nil, err
		}
		lines = append(lines, text)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(lines) != to-from {
		return nil, fmt.Errorf("receipt nodes %d..%d of program %s are unavailable", from, to, p.scope.ProgramID)
	}
	return lines, rows.Close()
}

// receiptLine renders one record as a single line. Markup characters stay
// literal because the document is read as text.
func receiptLine(record any) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
