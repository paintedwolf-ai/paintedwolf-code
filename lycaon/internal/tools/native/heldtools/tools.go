// Package heldtools reads and stops calls held past their foreground wait.
package heldtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// MaxAwait bounds how long held_result waits for a running call.
const MaxAwait = 30 * time.Second

// ResultTool reports a held call's state and, once settled, its result.
type ResultTool struct {
	Registry *heldcall.Registry
}

type resultPayload struct {
	Handle    string          `json:"handle"`
	Tool      string          `json:"tool"`
	State     string          `json:"state"`
	ElapsedMs int64           `json:"elapsed_ms"`
	Stopped   bool            `json:"stopped,omitempty"`
	Outcome   string          `json:"outcome,omitempty"`
	Codes     []string        `json:"codes,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

func (t *ResultTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if t.Registry == nil {
		return "", errors.New("held call registry not configured")
	}
	handle, _ := args["handle"].(string)
	handle = strings.TrimSpace(handle)
	wait := time.Duration(toolkit.ClampIntArg(args, "wait_ms", 0, 0, int(MaxAwait/time.Millisecond))) * time.Millisecond
	status, err := t.Registry.Await(ctx, tctx.SessionID, handle, wait)
	if errors.Is(err, heldcall.ErrNotFound) {
		return "", notFound(handle)
	}
	if err != nil {
		return "", err
	}
	if tctx.Out != nil {
		tctx.Out.OwnerRef = handle
	}
	tctx.SetDisplaySubject(heldSubject(status))
	payload := resultPayload{
		Handle: status.Handle, Tool: status.Tool, State: "running",
		ElapsedMs: status.Elapsed.Milliseconds(), Stopped: status.Stopped,
	}
	if settled := status.Settled; settled != nil {
		payload.State = "settled"
		payload.Outcome = string(settled.Outcome)
		payload.Codes = settled.Facts.Codes
		payload.Result = encodeResult(settled.Content)
	}
	encoded, err := surveyjson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("held_result encode: %w", err)
	}
	return string(encoded), nil
}

// encodeResult embeds a JSON result as itself and any other text as a string.
func encodeResult(content string) json.RawMessage {
	if trimmed := strings.TrimSpace(content); trimmed != "" && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	encoded, err := surveyjson.Marshal(content)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}

// StopTool cancels a held call that is still running.
type StopTool struct {
	Registry *heldcall.Registry
}

func (t *StopTool) Run(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if t.Registry == nil {
		return "", errors.New("held call registry not configured")
	}
	handle, _ := args["handle"].(string)
	handle = strings.TrimSpace(handle)
	if status, err := t.Registry.Status(tctx.SessionID, handle); err == nil {
		tctx.SetDisplaySubject(heldSubject(status))
	}
	result, err := t.Registry.Stop(tctx.SessionID, handle)
	if errors.Is(err, heldcall.ErrNotFound) {
		return "", notFound(handle)
	}
	if err != nil {
		return "", err
	}
	if tctx.Out != nil {
		tctx.Out.OwnerRef = handle
	}
	encoded, err := surveyjson.Marshal(toolkit.ProcessStopResult{
		Handle: result.Handle, StopRequested: result.Running, Running: result.Running,
	})
	if err != nil {
		return "", fmt.Errorf("held_stop encode: %w", err)
	}
	return string(encoded), nil
}

func notFound(handle string) error {
	return &toolrejection.ToolReject{Code: "HELD_CALL_NOT_FOUND", Data: map[string]any{"handle": handle}}
}

// Register adds the held-call tools to a registry.
func Register(reg *tools.DefaultRegistry, held *heldcall.Registry) error {
	if err := reg.Register("held_result", (&ResultTool{Registry: held}).Run); err != nil {
		return err
	}
	return reg.Register("held_stop", (&StopTool{Registry: held}).Run)
}

func heldSubject(status heldcall.Status) string {
	if status.DisplayTitle != "" {
		return status.DisplayTitle
	}
	return strings.ReplaceAll(status.Tool, "_", " ")
}
