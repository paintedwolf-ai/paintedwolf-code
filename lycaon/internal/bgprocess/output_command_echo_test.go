package bgprocess

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

const commandEchoReference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"

func commandEchoHarness(t *testing.T, size int) *projectionHarness {
	t.Helper()
	h := newProjectionHarness(t, size)
	h.proc.originTool = "command"
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Secret: projectionSecret, RuleID: secretmatch.ManagedRuleID,
			Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
			NonDisclosable: true, Reference: commandEchoReference}}
	})
	h.reg.Output.SetCaptureProjector(captureprojection.New(matcher, nil))
	return h
}

func TestCommandOutputProjectsOneReferenceAcrossChunksAndRawCursors(t *testing.T) {
	h := commandEchoHarness(t, DefaultRingBufferBytes)
	h.write("stdout", "before: ")
	h.write("stdout", projectionSecret[:8])
	h.write("stderr", projectionSecret[8:])
	h.write("stderr", " :after")
	whole := "before: " + projectionSecret + " :after"
	for _, cursor := range []int64{0, 8, 12, 16} {
		snapshot, err := h.reg.Output.ReadCommandOutput(t.Context(), "s1", "h1", cursor)
		if err != nil {
			t.Fatalf("read command output: %v", err)
		}
		text := joinOutputChunks(snapshot.Output.Chunks)
		if strings.Contains(text, projectionSecret) || strings.Count(text, commandEchoReference) != 1 {
			t.Fatalf("cursor=%d: wrong projection %q", cursor, text)
		}
		if snapshot.Output.Next != int64(len(whole)) || snapshot.Output.From != cursor {
			t.Fatalf("cursor=%d: raw cursor changed: %+v", cursor, snapshot.Output)
		}
		for _, chunk := range snapshot.Output.Chunks {
			if chunk.Stream != "stdout" && chunk.Stream != "stderr" {
				t.Fatalf("stream attribution lost: %+v", chunk)
			}
		}
	}
	raw, err := h.reg.Output.ReadRawOutput("s1", "h1", 0)
	if err != nil || joinOutputChunks(raw.Output.Chunks) != whole {
		t.Fatalf("publication mutated raw output: err=%v output=%+v", err, raw.Output)
	}
}

func TestCommandOutputMasksEmbeddedEncodingBeforeChunkAndCursorSelection(t *testing.T) {
	h := commandEchoHarness(t, DefaultRingBufferBytes)
	encoded := base64.StdEncoding.EncodeToString([]byte("!" + projectionSecret + "?suffix"))
	h.write("stdout", encoded[:8])
	h.write("stdout", encoded[8:20])
	h.write("stderr", encoded[20:])
	for _, cursor := range []int64{0, 8, 12, 20} {
		snapshot, err := h.reg.Output.ReadCommandOutput(t.Context(), "s1", "h1", cursor)
		if err != nil {
			t.Fatalf("read command output: %v", err)
		}
		text := joinOutputChunks(snapshot.Output.Chunks)
		if !strings.Contains(text, "[REDACTED]") || strings.Contains(text, commandEchoReference) || strings.Contains(text, encoded[8:20]) {
			t.Fatalf("cursor=%d: embedded value exposed or referenced: %q", cursor, text)
		}
		if snapshot.Output.Next != int64(len(encoded)) {
			t.Fatalf("screened length replaced raw cursor: %+v", snapshot.Output)
		}
	}
}

func TestCommandOutputWithholdsEvictedManagedTail(t *testing.T) {
	h := commandEchoHarness(t, 28)
	h.write("stdout", strings.Repeat("A", 16))
	h.write("stdout", "capture-")
	h.write("stderr", "secret-value")
	h.write("stdout", strings.Repeat("B", 12))
	h.write("stdout", strings.Repeat("C", 12))
	snapshot, err := h.reg.Output.ReadCommandOutput(t.Context(), "s1", "h1", 0)
	if err != nil {
		t.Fatalf("read command output: %v", err)
	}
	text := joinOutputChunks(snapshot.Output.Chunks)
	if !snapshot.Output.Truncated || strings.Contains(text, "secret-value") {
		t.Fatalf("evicted managed tail survived: %+v", snapshot.Output)
	}
}

func TestCommandOutputNeverFallsBackToRawOnProjectionFailure(t *testing.T) {
	h := commandEchoHarness(t, DefaultRingBufferBytes)
	h.write("stdout", projectionSecret)
	h.reg.Output.SetCaptureProjector(nil)
	snapshot, err := h.reg.Output.ReadCommandOutput(t.Context(), "s1", "h1", 0)
	if err != nil {
		t.Fatalf("read command output: %v", err)
	}
	if text := joinOutputChunks(snapshot.Output.Chunks); text != captureUnavailableText {
		t.Fatalf("missing projector exposed raw output: %q", text)
	}
}

func TestCommandOutputReleasesProjectionMemoryAndCoalescesWithheldRanges(t *testing.T) {
	h := commandEchoHarness(t, DefaultRingBufferBytes)
	h.proc.originTool = "verify"
	h.write("stdout", projectionSecret)
	for i := 0; i < 20; i++ {
		snapshot, err := h.reg.Output.ReadCommandOutput(t.Context(), "s1", "h1", 0)
		if err != nil || !strings.Contains(joinOutputChunks(snapshot.Output.Chunks), commandEchoReference) {
			t.Fatalf("repeat projection: %+v %v", snapshot, err)
		}
	}
	memory, err := h.reg.Output.screenMemory()
	if err != nil || memory.InUse().Working != 0 || len(h.proc.withheld) != 1 {
		t.Fatalf("projection retained scratch: err=%v usage=%+v spans=%d", err, memory.InUse(), len(h.proc.withheld))
	}
	view, err := h.reg.Output.ReadOutput(t.Context(), "s1", "h1")
	if err != nil || len(view.Chunks) != 1 || !strings.Contains(view.Chunks[0].Text, commandEchoReference) {
		t.Fatalf("Den projection: %+v %v", view, err)
	}
	snapshot, err := h.reg.Snapshot(t.Context(), "s1", "h1", 0)
	if err != nil || !strings.Contains(snapshot.Output, commandEchoReference) {
		t.Fatalf("snapshot projection: %+v %v", snapshot, err)
	}
}
