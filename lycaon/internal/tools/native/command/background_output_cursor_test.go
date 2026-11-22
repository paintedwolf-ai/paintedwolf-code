package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func loadCommandOutputSchemas(t *testing.T) (rawSchema, projectedSchema map[string]any) {
	t.Helper()
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "load catalog", err)
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load effective tool schemas", err)
	meta, ok := cfg.ToolMeta("command_output")
	if !ok {
		t.Fatal("command_output schema missing")
	}
	return meta.ArgsSchema, toolschema.ProjectArgsSchema(meta.ArgsSchema)
}

func newScreenedBackgroundRegistry(t *testing.T) *bgprocess.Registry {
	t.Helper()
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() {
		_ = reg.Lifecycle.Close(context.Background())
	})
	return reg
}

func joinedDecodedChunks(t *testing.T, decoded map[string]any) string {
	t.Helper()
	rawChunks, ok := decoded["chunks"].([]any)
	if !ok {
		t.Fatalf("chunks = %T (%v), want []any", decoded["chunks"], decoded["chunks"])
	}
	var b strings.Builder
	for _, item := range rawChunks {
		chunk, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("chunk = %T, want object", item)
		}
		text, _ := chunk["text"].(string)
		b.WriteString(text)
	}
	return b.String()
}

func TestCommandOutputCursorRoundTripThroughProductionValidation(t *testing.T) {
	rawSchema, projectedSchema := loadCommandOutputSchemas(t)
	reg := newScreenedBackgroundRegistry(t)
	sessionID := "cursor-roundtrip-session"
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}

	phase1Ready := make(chan struct{})
	releasePhase2 := make(chan struct{})
	handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID:   sessionID,
		CommandLine: "streamed-phases",
		OriginTool:  "command",
		ToolCallID:  "call-1",
		Run: func(_ context.Context, stdout, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			if _, wErr := io.WriteString(stdout, "alpha\n"); wErr != nil {
				return bgprocess.ProgramJobResult{ExitCode: 1}, wErr
			}
			close(phase1Ready)
			<-releasePhase2
			if _, wErr := io.WriteString(stdout, "beta\n"); wErr != nil {
				return bgprocess.ProgramJobResult{ExitCode: 1}, wErr
			}
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start background program", err)
	<-phase1Ready

	outputTool := &CommandOutputTool{Registry: reg}

	// First read starts at integer cursor 0.
	firstArgs := map[string]any{"handle": handle, "cursor": 0}
	for label, schema := range map[string]map[string]any{"raw": rawSchema, "projected": projectedSchema} {
		if reject := tools.ValidateCallArguments("command_output", firstArgs, schema, tctx); reject != nil {
			t.Fatalf("first read rejected by %s schema: %#v", label, reject)
		}
	}
	firstJSON, err := outputTool.Run(t.Context(), firstArgs, tctx)
	testutil.FailErr(t, "first command_output read", err)

	var firstResult map[string]any
	testutil.FailErr(t, "decode first result", json.Unmarshal([]byte(firstJSON), &firstResult))
	if firstResult["cursor"] != "0" || firstResult["next_cursor"] != "6" || firstResult["running"] != true {
		t.Fatalf("unexpected first result cursor state: %#v", firstResult)
	}
	if got := joinedDecodedChunks(t, firstResult); got != "alpha\n" {
		t.Fatalf("first read chunks = %q, want %q", got, "alpha\n")
	}

	close(releasePhase2)
	_, _, err = reg.Lifecycle.Wait(t.Context(), sessionID, handle)
	testutil.FailErr(t, "wait for background program", err)

	// Feed the unchanged returned next_cursor JSON value into the subsequent request.
	secondArgs := map[string]any{"handle": handle, "cursor": firstResult["next_cursor"]}
	for label, schema := range map[string]map[string]any{"raw": rawSchema, "projected": projectedSchema} {
		if reject := tools.ValidateCallArguments("command_output", secondArgs, schema, tctx); reject != nil {
			t.Fatalf("subsequent read with unchanged next_cursor (%T %v) rejected by %s schema: %#v", firstResult["next_cursor"], firstResult["next_cursor"], label, reject)
		}
	}
	secondJSON, err := outputTool.Run(t.Context(), secondArgs, tctx)
	testutil.FailErr(t, "subsequent command_output read", err)

	var secondResult map[string]any
	testutil.FailErr(t, "decode second result", json.Unmarshal([]byte(secondJSON), &secondResult))
	if secondResult["cursor"] != "6" || secondResult["next_cursor"] != "11" || secondResult["running"] != false {
		t.Fatalf("unexpected second result cursor state: %#v", secondResult)
	}
	if got := joinedDecodedChunks(t, secondResult); got != "beta\n" {
		t.Fatalf("subsequent read chunks = %q, want only %q", got, "beta\n")
	}
}

func TestCommandOutputLargeOffsetRetainsExactPrecision(t *testing.T) {
	const largeOffset int64 = (1 << 53) + 37
	if int64(float64(largeOffset)) == largeOffset {
		t.Fatalf("largeOffset %d must exceed IEEE-754 float64 exact integer precision", largeOffset)
	}
	wantDecimal := strconv.FormatInt(largeOffset, 10)

	rawSchema, projectedSchema := loadCommandOutputSchemas(t)
	reg := newScreenedBackgroundRegistry(t)
	sessionID := "large-offset-session"
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}

	gate := make(chan struct{})
	defer close(gate)
	handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID:   sessionID,
		CommandLine: "long-stream",
		OriginTool:  "command",
		ToolCallID:  "call-large",
		Run: func(_ context.Context, stdout, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			_, _ = io.WriteString(stdout, "hi\n")
			<-gate
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start background program", err)

	// Encode a real commandOutputResult at a >2^53 offset and unmarshal with standard encoding/json.
	priorPayload := commandOutputResult{
		commandOutput: commandOutputOf(bgprocess.RawOutput{
			Handle:  handle,
			From:    largeOffset - 5,
			Next:    largeOffset,
			Chunks:  []bgprocess.OutputChunk{},
			Running: true,
		}, ""),
	}
	priorBytes, err := surveyjson.Marshal(priorPayload)
	testutil.FailErr(t, "encode large-offset command_output payload", err)

	var priorDecoded map[string]any
	testutil.FailErr(t, "decode large-offset command_output payload", json.Unmarshal(priorBytes, &priorDecoded))

	nextArgs := map[string]any{"handle": handle, "cursor": priorDecoded["next_cursor"]}
	for label, schema := range map[string]map[string]any{"raw": rawSchema, "projected": projectedSchema} {
		if reject := tools.ValidateCallArguments("command_output", nextArgs, schema, tctx); reject != nil {
			t.Fatalf("large-offset continuation rejected by %s schema: %#v", label, reject)
		}
	}

	outputTool := &CommandOutputTool{Registry: reg}
	outJSON, err := outputTool.Run(t.Context(), nextArgs, tctx)
	testutil.FailErr(t, "run command_output at large offset", err)

	var outDecoded map[string]any
	testutil.FailErr(t, "decode large-offset read result", json.Unmarshal([]byte(outJSON), &outDecoded))
	if outDecoded["cursor"] != wantDecimal {
		t.Fatalf("cursor = %v, want exact %q", outDecoded["cursor"], wantDecimal)
	}

	// Also verify json.Number (UseNumber decoding) preserves exact 64-bit integer offsets.
	dec := json.NewDecoder(bytes.NewBufferString(`{"handle":"` + handle + `","cursor":` + wantDecimal + `}`))
	dec.UseNumber()
	var numArgs map[string]any
	testutil.FailErr(t, "decode json.Number args", dec.Decode(&numArgs))
	if reject := tools.ValidateCallArguments("command_output", numArgs, projectedSchema, tctx); reject != nil {
		t.Fatalf("json.Number cursor rejected by projected schema: %#v", reject)
	}
	numOutJSON, err := outputTool.Run(t.Context(), numArgs, tctx)
	testutil.FailErr(t, "run command_output with json.Number cursor", err)
	var numOutDecoded map[string]any
	testutil.FailErr(t, "decode json.Number read result", json.Unmarshal([]byte(numOutJSON), &numOutDecoded))
	if numOutDecoded["cursor"] != wantDecimal {
		t.Fatalf("json.Number cursor = %v, want exact %q", numOutDecoded["cursor"], wantDecimal)
	}
}

func TestCommandOutputRejectsInvalidCursorsLocally(t *testing.T) {
	_, projectedSchema := loadCommandOutputSchemas(t)
	reg := newScreenedBackgroundRegistry(t)
	sessionID := "invalid-cursor-session"
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}

	gate := make(chan struct{})
	defer close(gate)
	handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID:   sessionID,
		CommandLine: "idle",
		OriginTool:  "command",
		ToolCallID:  "call-invalid",
		Run: func(_ context.Context, _, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			<-gate
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start background program", err)

	for _, bad := range []any{-1, 1.5, "", "abc", "-5", "01", " 5"} {
		args := map[string]any{"handle": handle, "cursor": bad}
		reject := tools.ValidateCallArguments("command_output", args, projectedSchema, tctx)
		if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("ValidateCallArguments(%#v) = %#v, want TOOL_ARGS_INVALID", bad, reject)
		}
	}

	outputTool := &CommandOutputTool{Registry: reg}
	for _, bad := range []any{-1, 1.5, "", "abc", "-5", "01", "9999999999999999999", true} {
		_, runErr := outputTool.Run(t.Context(), map[string]any{"handle": handle, "cursor": bad}, tctx)
		var reject *toolrejection.ToolReject
		if !errors.As(runErr, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("Run(%#v) err = %v, want TOOL_ARGS_INVALID ToolReject", bad, runErr)
		}
	}
}
