package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const deliveryWindowKind = "command_output_window"
const deliveryWindowVersion = 1

type deliveryWindowDescriptor struct {
	Kind    string                         `json:"kind"`
	Version int                            `json:"version"`
	Window  api.CommandProgramOutputWindow `json:"window"`
}

func deliveryWindowReference(program, stream string) string {
	return "output:program:" + program + ":window:" + stream
}

// AttachDeliveryWindow retains a bounded presentation descriptor through P10's
// existing detail-body lifecycle. Its preview contains the complete descriptor,
// never execution authority or bytes to feed back into a program.
func (r *ProgramOutputRetention) AttachDeliveryWindow(ctx context.Context, program string, window api.CommandProgramOutputWindow) (err error) {
	if err := validateDeliveryWindow(program, window); err != nil {
		return err
	}
	descriptor := deliveryWindowDescriptor{Kind: deliveryWindowKind, Version: deliveryWindowVersion, Window: window}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	if len(data) > maxOutputPreviewBytes {
		return fmt.Errorf("delivery descriptor exceeds receipt preview ceiling")
	}
	reference := deliveryWindowReference(program, window.Stream)
	spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(bytes.NewReader(data))), tooloutput.StreamSpillOptions{
		Store: r.store, ClaimID: reference, MaximumBytes: maxOutputPreviewBytes, PreviewBytes: maxOutputPreviewBytes, Admission: r.resources, WorkingMemory: r.resources, RetainedState: r.resources,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, spill.Close()) }()
	_, err = r.Attach(ctx, program, RetainedOutput{Stream: "detail", Reference: reference, Complete: true}, spill)
	return err
}

func validateDeliveryWindow(program string, w api.CommandProgramOutputWindow) error {
	if (w.Stream != "stdout" && w.Stream != "stderr") || w.Ref != "output:program:"+program+":"+w.Stream || w.OutputPath != "@output/"+program+"."+w.Stream || w.TotalBytes < 0 || w.TotalLines < 0 || w.InlineBudgetBytes < 1 || w.HeadEndByte < 0 || w.TailStartByte < 0 || w.HeadEndLine < 0 || w.TailStartLine < 0 || w.OmittedBytes < 0 || w.OmittedLines < 0 {
		return fmt.Errorf("invalid delivery window identity or bounds")
	}
	if w.Truncated && (w.TailStartByte < w.HeadEndByte || w.TailStartByte > w.TotalBytes || w.OmittedBytes != w.TailStartByte-w.HeadEndByte || w.OmittedLines > w.TotalLines) {
		return fmt.Errorf("invalid delivery window range")
	}
	if w.Limit != nil && (w.Complete || w.Limit.Code != "COMMAND_LIMIT_EXCEEDED" || w.Limit.LimitID == "" || w.Limit.Maximum < 1 || w.Limit.Observed <= w.Limit.Maximum) {
		return fmt.Errorf("invalid delivery limit")
	}
	return nil
}

func readDeliveryWindows(ctx context.Context, tx *sql.Tx, program string) ([]api.CommandProgramOutputWindow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.reference,o.preview,o.preview_truncated,o.complete,length(CAST(o.preview AS BLOB)),json_extract(o.blob_json,'$.Size'),f.reference,json_extract(f.blob_json,'$.Size'),f.complete FROM invocation_program_outputs o JOIN invocation_program_outputs f ON f.program_id=o.program_id AND f.execution_id IS NULL AND f.stream IN ('stdout','stderr') AND o.reference='output:program:'||f.program_id||':window:'||f.stream WHERE o.program_id=? AND o.execution_id IS NULL AND o.stream='detail' AND o.reference IN (?,?) ORDER BY CASE f.stream WHEN 'stdout' THEN 0 ELSE 1 END`, program, deliveryWindowReference(program, "stdout"), deliveryWindowReference(program, "stderr"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []api.CommandProgramOutputWindow
	for rows.Next() {
		var reference, preview, fullReference string
		var truncated, complete, fullComplete bool
		var previewBytes, bodyBytes, fullBytes int64
		if err := rows.Scan(&reference, &preview, &truncated, &complete, &previewBytes, &bodyBytes, &fullReference, &fullBytes, &fullComplete); err != nil {
			return nil, err
		}
		if truncated || !complete || previewBytes != bodyBytes || bodyBytes > maxOutputPreviewBytes {
			return nil, fmt.Errorf("delivery descriptor is incomplete")
		}
		var descriptor deliveryWindowDescriptor
		decoder := json.NewDecoder(bytes.NewBufferString(preview))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&descriptor); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("delivery descriptor has trailing data")
		}
		if descriptor.Kind != deliveryWindowKind || descriptor.Version != deliveryWindowVersion || reference != deliveryWindowReference(program, descriptor.Window.Stream) {
			return nil, fmt.Errorf("unknown delivery descriptor identity")
		}
		if descriptor.Window.Ref != fullReference || descriptor.Window.TotalBytes != fullBytes || descriptor.Window.Complete != fullComplete {
			return nil, fmt.Errorf("delivery descriptor differs from its retained stream")
		}
		if err := validateDeliveryWindow(program, descriptor.Window); err != nil {
			return nil, err
		}
		out = append(out, descriptor.Window)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
