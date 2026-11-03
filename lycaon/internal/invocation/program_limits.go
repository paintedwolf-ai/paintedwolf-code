package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type programReceiptLimits struct {
	events, bytes, arguments, nodes, children int64
	terminalEvents, terminalBytes             int64
	parentEvents, parentBytes, progressEvents int64
	progressBytes                             int64
}

type traceChargeMode int

const (
	traceChargeNonTerminal traceChargeMode = iota
	traceChargeChildStart
	traceChargeProgressStart
	traceChargeChildSettlement
	traceChargeProgressSettlement
	traceChargeProgramSettlement
)

var receiptLimits = sync.OnceValues(func() (programReceiptLimits, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return programReceiptLimits{}, err
	}
	limits := programReceiptLimits{}
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "trace_events":
			limits.events = limit.Maximum
		case "trace_bytes":
			limits.bytes = limit.Maximum
		case "argument_bytes":
			limits.arguments = limit.Maximum
		case "ast_nodes":
			limits.nodes = limit.Maximum
		case "child_executions":
			limits.children = limit.Maximum
		}
	}
	limits.terminalEvents = catalog.Language.SettlementReserve["per_live_child_events"]
	limits.terminalBytes = catalog.Language.SettlementReserve["per_live_child_bytes"]
	limits.parentEvents = catalog.Language.SettlementReserve["parent_events"]
	limits.parentBytes = catalog.Language.SettlementReserve["parent_bytes"]
	limits.progressEvents = catalog.Language.SettlementReserve["progress_events"]
	limits.progressBytes = catalog.Language.SettlementReserve["progress_bytes"]
	if limits.events < 1 || limits.bytes < 1 || limits.arguments < 1 || limits.nodes < 1 || limits.children < 1 || limits.terminalEvents < 1 || limits.terminalBytes < 1 || limits.parentEvents < 1 || limits.parentBytes < 1 || limits.progressEvents < 1 || limits.progressBytes < 1 {
		return limits, fmt.Errorf("compiled language is missing receipt resource bounds")
	}
	return limits, nil
})

func chargeProgramTrace(ctx context.Context, tx *sql.Tx, programID string, events, bytes int64, mode traceChargeMode) error {
	if events < 0 || bytes < 0 {
		return errors.New("program trace charge must be non-negative")
	}
	limits, err := receiptLimits()
	if err != nil {
		return err
	}
	var usedEvents, usedBytes, liveParent, liveProgress, liveChildren, totalChildren int64
	if err := tx.QueryRowContext(ctx, `SELECT p.trace_events,p.trace_bytes,CASE WHEN p.state IN ('running','waiting') THEN 1 ELSE 0 END,(SELECT count(*) FROM invocation_program_progress WHERE program_id=p.id AND state='running'),(SELECT count(*) FROM invocation_program_executions WHERE program_id=p.id AND state IN ('prepared','waiting','running')),(SELECT COALESCE(MAX(ordinal),0) FROM invocation_program_executions WHERE program_id=p.id) FROM invocation_programs p WHERE p.id=?`, programID).Scan(&usedEvents, &usedBytes, &liveParent, &liveProgress, &liveChildren, &totalChildren); err != nil {
		return fmt.Errorf("read program trace budget: %w", err)
	}
	switch mode {
	case traceChargeNonTerminal:
		// Non-terminal writes keep every live owner's settlement reserve.
	case traceChargeChildStart:
		if totalChildren+1 > limits.children {
			return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": "child_executions", "resource_unit": "children", "maximum": limits.children, "observed": totalChildren + 1})
		}
		liveChildren++
	case traceChargeProgressStart:
		liveProgress = 1
	case traceChargeChildSettlement:
		if events > limits.terminalEvents || bytes > limits.terminalBytes {
			return errors.New("child settlement exceeds reserved terminal budget")
		}
		if liveChildren > 0 {
			liveChildren--
		}
	case traceChargeProgressSettlement:
		if events > limits.progressEvents || bytes > limits.progressBytes {
			return errors.New("progress settlement exceeds reserved terminal budget")
		}
		liveProgress = 0
	case traceChargeProgramSettlement:
		if events > limits.parentEvents*(1+liveChildren+liveProgress) || bytes > limits.parentBytes*(1+liveChildren+liveProgress) {
			return errors.New("program settlement exceeds reserved terminal budget")
		}
		liveParent, liveProgress, liveChildren = 0, 0, 0
	}
	reservedEvents := liveParent*limits.parentEvents + liveProgress*limits.progressEvents + liveChildren*limits.terminalEvents
	reservedBytes := liveParent*limits.parentBytes + liveProgress*limits.progressBytes + liveChildren*limits.terminalBytes
	if usedEvents+events+reservedEvents > limits.events {
		return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": "trace_events", "resource_unit": "events", "maximum": limits.events, "observed": usedEvents + events + reservedEvents})
	}
	if usedBytes+bytes+reservedBytes > limits.bytes {
		return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": "trace_bytes", "resource_unit": "bytes", "maximum": limits.bytes, "observed": usedBytes + bytes + reservedBytes})
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_programs SET trace_events=trace_events+?,trace_bytes=trace_bytes+? WHERE id=?`, events, bytes, programID)
	return err
}
