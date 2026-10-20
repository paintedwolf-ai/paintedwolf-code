package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramSource records an exact capture. Recording input is not a delivered read.
type ProgramSource struct {
	UnitID          string
	Pin             toolcontract.SourcePin
	DetailReference string
}

// ProgramNode describes known syntax, not a speculative loop iteration.
type ProgramNode struct {
	ID           string
	SourceUnitID string
	Span         toolcontract.SourceSpan
}

// ProgramStart retains a validated plan and exact source before child admission.
// PlanReference and SourceReference name screened durable bodies, not inline text.
type ProgramStart struct {
	Version       ProgramVersion
	PlanReference string
	SourceExact   bool
	Sources       []ProgramSource
	Nodes         []ProgramNode
}

func (s ProgramStart) validate() error {
	if err := s.Version.Validate(); err != nil {
		return err
	}
	if s.PlanReference == "" || len(s.Sources) == 0 || len(s.Nodes) == 0 {
		return fmt.Errorf("program requires retained plan, source and static nodes")
	}
	sources := make(map[string]toolcontract.SourcePin, len(s.Sources))
	for _, source := range s.Sources {
		pin := source.Pin
		if source.UnitID == "" || source.DetailReference == "" || pin.SourceID == "" || pin.Revision == "" || !validDigest(pin.SHA256) || pin.StartByte < 0 || pin.EndByte < pin.StartByte {
			return fmt.Errorf("invalid program source identity")
		}
		if pin.Representation != toolcontract.UTF8SourceBytes {
			return fmt.Errorf("program source requires exact UTF-8 syntax bytes")
		}
		if _, ok := sources[source.UnitID]; ok {
			return fmt.Errorf("duplicate program source")
		}
		sources[source.UnitID] = pin
	}
	root, ok := sources[s.Version.SourceUnitID]
	if !ok || root.SHA256 != s.Version.SourceDigest {
		return fmt.Errorf("root source identity differs from submitted program")
	}
	nodes := make(map[string]bool, len(s.Nodes))
	for _, node := range s.Nodes {
		source, ok := sources[node.SourceUnitID]
		if !ok || node.ID == "" || nodes[node.ID] || node.Span.Line < 1 || node.Span.Column < 1 || node.Span.StartByte < source.StartByte || node.Span.EndByte < node.Span.StartByte || node.Span.EndByte > source.EndByte {
			return fmt.Errorf("invalid or duplicate static program node")
		}
		nodes[node.ID] = true
	}
	return nil
}

// BeginProgram attaches to the real running model receipt in one writer transaction.
// It neither opens another model call nor settles the outer invocation.
func (r *SQLRecorder) BeginProgram(ctx context.Context, in ProgramStart) error {
	if err := in.validate(); err != nil {
		return err
	}
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM invocation_receipts WHERE id = ?", in.Version.ParentReceiptID).Scan(&state); err != nil {
		return fmt.Errorf("program parent: %w", err)
	}
	if state != "running" {
		return fmt.Errorf("program parent is not running")
	}
	v := in.Version
	_, err = tx.ExecContext(ctx, `INSERT INTO invocation_programs (id,parent_receipt_id,root_execution_id,language_version,catalog_digest,source_unit_id,source_digest,source_ref,source_exact,plan_ref,state,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,'running',?)`, v.ProgramID, v.ParentReceiptID, v.RootExecutionID, v.LanguageVersion, v.CatalogDigest, v.SourceUnitID, v.SourceDigest, v.SourceReference, in.SourceExact, in.PlanReference, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("begin program: %w", err)
	}
	for _, source := range in.Sources {
		pin := source.Pin
		_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_sources (program_id,source_unit_id,source_id,revision,representation,sha256,start_byte,end_byte,detail_ref) VALUES (?,?,?,?,?,?,?,?,?)`, v.ProgramID, source.UnitID, pin.SourceID, pin.Revision, pin.Representation, pin.SHA256, pin.StartByte, pin.EndByte, source.DetailReference)
		if err != nil {
			return err
		}
	}
	for _, node := range in.Nodes {
		_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_nodes (program_id,node_id,source_unit_id,start_byte,end_byte,line,column_number) VALUES (?,?,?,?,?,?,?)`, v.ProgramID, node.ID, node.SourceUnitID, node.Span.StartByte, node.Span.EndByte, node.Span.Line, node.Span.Column)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PrepareChild records the independently addressed dynamic execution before
// approval, including rejected admission and held calls. Reuse its ID on resume.
func (r *SQLRecorder) PrepareChild(ctx context.Context, in ChildStart) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateChildParent(ctx, tx, in); err != nil {
		return err
	}
	facts := in.Definition.Facts()
	identity := in.Invocation.Identity
	scope, err := json.Marshal(in.Invocation.Scope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_executions (id,program_id,node_id,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,definition_json,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?,?)`, identity.ExecutionID, identity.ProgramID, identity.NodeID, identity.ParentExecutionID, in.Invocation.ActorID, string(scope), facts.Identity, in.Definition.Digest(), in.Definition.Serialized(), in.ArgumentsDigest, in.ArgumentsReference, in.Canonical, facts.Contract.Owner, facts.Contract.Lifecycle.String(), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prepare program child: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_program_nodes SET state='reached' WHERE program_id=? AND node_id=?`, identity.ProgramID, identity.NodeID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validateChildParent(ctx context.Context, tx *sql.Tx, in ChildStart) error {
	var parent, root, session, state, catalog, unit string
	var start, end int64
	var line, column int
	id := in.Invocation.Identity
	err := tx.QueryRowContext(ctx, `SELECT p.parent_receipt_id,p.root_execution_id,r.session_id,p.state,p.catalog_digest,n.source_unit_id,n.start_byte,n.end_byte,n.line,n.column_number FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id JOIN invocation_program_nodes n ON n.program_id=p.id AND n.node_id=? WHERE p.id=?`, id.NodeID, id.ProgramID).Scan(&parent, &root, &session, &state, &catalog, &unit, &start, &end, &line, &column)
	if err != nil {
		return fmt.Errorf("child program identity: %w", err)
	}
	v := in.Invocation
	if parent != in.ParentReceiptID || session != v.SessionID || catalog != v.CatalogDigest || unit != v.SourceUnitID || start != v.Span.StartByte || end != v.Span.EndByte || line != v.Span.Line || column != v.Span.Column || (state != "running" && state != "waiting") {
		return fmt.Errorf("child differs from its retained program")
	}
	if id.ParentExecutionID == root {
		return nil
	}
	var present int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM invocation_program_executions WHERE program_id=? AND id=?`, id.ProgramID, id.ParentExecutionID).Scan(&present)
	if err != nil {
		return fmt.Errorf("child requires an actual execution parent: %w", err)
	}
	return nil
}
