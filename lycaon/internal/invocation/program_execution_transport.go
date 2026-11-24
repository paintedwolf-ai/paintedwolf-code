package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const executionTransportKind = "git_transport_evidence"
const executionTransportVersion = 1
const maxRetainedTransportAttempts = 16

type executionTransportDescriptor struct {
	Kind      string                        `json:"kind"`
	Version   int                           `json:"version"`
	Transport api.CommandExecutionTransport `json:"transport"`
}

func executionTransportReference(execution string) string {
	return "output:" + execution + ":transport"
}

// AttachExecutionTransport persists the verified transport mediation summary and attempts for one child execution.
func (r *ProgramOutputRetention) AttachExecutionTransport(ctx context.Context, program, execution string, transport api.CommandExecutionTransport) (err error) {
	if execution == "" || transport.Cause == "" {
		return nil
	}
	bounded := transport
	if len(bounded.Attempts) > maxRetainedTransportAttempts {
		bounded.Attempts = slices.Clone(bounded.Attempts[:maxRetainedTransportAttempts])
	}
	if err := validateExecutionTransport(bounded); err != nil {
		return err
	}
	descriptor := executionTransportDescriptor{
		Kind:      executionTransportKind,
		Version:   executionTransportVersion,
		Transport: bounded,
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	if len(data) > maxOutputPreviewBytes {
		return fmt.Errorf("execution transport descriptor exceeds receipt preview ceiling")
	}
	reference := executionTransportReference(execution)
	spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(bytes.NewReader(data))), tooloutput.StreamSpillOptions{
		Store: r.store, ClaimID: program + ":" + reference, MaximumBytes: maxOutputPreviewBytes, PreviewBytes: maxOutputPreviewBytes,
		Admission: r.resources, WorkingMemory: r.resources, RetainedState: r.resources,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, spill.Close()) }()
	_, err = r.Attach(ctx, program, RetainedOutput{ExecutionID: execution, Stream: "detail", Reference: reference, Complete: true}, spill)
	return err
}

func validateExecutionTransport(t api.CommandExecutionTransport) error {
	switch t.Cause {
	case "established", "setup_failed", "lease_ended", "endpoint_denied", "dial_failed", "invalid_request", "unknown":
	default:
		return fmt.Errorf("invalid execution transport cause %q", t.Cause)
	}
	if t.Port < 0 || t.Port > 65535 {
		return fmt.Errorf("invalid execution transport port %d", t.Port)
	}
	for _, a := range t.Attempts {
		if a.Ordinal < 1 || a.Stage == "" || a.ReplyCode < 0 || a.ReplyCode > 5 || a.Port < 0 || a.Port > 65535 {
			return fmt.Errorf("invalid execution transport attempt")
		}
		switch a.Outcome {
		case "established", "setup_failed", "lease_ended", "endpoint_denied", "dial_failed", "invalid_request":
		default:
			return fmt.Errorf("invalid execution transport attempt outcome %q", a.Outcome)
		}
	}
	return nil
}

func readExecutionTransports(ctx context.Context, tx *sql.Tx, program string, executions []api.CommandExecutionReceipt) error {
	if len(executions) == 0 {
		return nil
	}
	index := make(map[string]int, len(executions))
	ids := make([]string, len(executions))
	for i := range executions {
		index[executions[i].ID] = i
		ids[i] = executions[i].ID
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.execution_id,o.reference,o.preview,o.preview_truncated,o.complete,length(CAST(o.preview AS BLOB)),json_extract(o.blob_json,'$.Size') FROM invocation_program_outputs o WHERE o.program_id=? AND o.execution_id IN (SELECT value FROM json_each(?)) AND o.stream='detail' AND o.reference='output:'||o.execution_id||':transport'`+publishedProgramOutput+` ORDER BY o.execution_id`, program, string(encoded))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var execution, reference, preview string
		var truncated, complete bool
		var previewBytes, bodyBytes int64
		if err := rows.Scan(&execution, &reference, &preview, &truncated, &complete, &previewBytes, &bodyBytes); err != nil {
			return err
		}
		if truncated || !complete || previewBytes != bodyBytes || bodyBytes > maxOutputPreviewBytes {
			return fmt.Errorf("execution transport descriptor is incomplete")
		}
		var descriptor executionTransportDescriptor
		decoder := json.NewDecoder(bytes.NewBufferString(preview))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&descriptor); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("execution transport descriptor has trailing data")
		}
		if descriptor.Kind != executionTransportKind || descriptor.Version != executionTransportVersion || reference != executionTransportReference(execution) {
			return fmt.Errorf("unknown execution transport descriptor identity")
		}
		if err := validateExecutionTransport(descriptor.Transport); err != nil {
			return err
		}
		transport := descriptor.Transport
		executions[index[execution]].Transport = &transport
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}
