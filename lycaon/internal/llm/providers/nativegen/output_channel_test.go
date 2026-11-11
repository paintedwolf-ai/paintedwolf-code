package nativegen

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestThoughtChannelSeparatesReasoningFromAnswerAndTools(t *testing.T) {
	d := newOutputDecoder(modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "command"}}})
	for _, part := range []struct {
		id   uint32
		text string
	}{
		{100, "<|channel>"}, {500, "tho"}, {501, "ught\n"},
		{502, "Check é and literal <|tool_call> text."}, {101, "<channel|>"},
		{48, "<|tool_call>"}, {503, "call:command{cmd:"}, {52, `<|"|>`},
		{504, "pwd"}, {52, `<|"|>`}, {505, "}"}, {49, "<tool_call|>"},
	} {
		visible, err := d.step(&part.id, part.text)
		testutil.FailErr(t, "decode thought and tool", err)
		if visible != "" {
			t.Fatalf("non-answer content became visible: %q", visible)
		}
	}
	if d.reasoning != "Check é and literal <|tool_call> text." {
		t.Fatalf("reasoning = %q", d.reasoning)
	}
	id := uint32(506)
	visible, err := d.step(&id, "Done.")
	testutil.FailErr(t, "decode answer", err)
	calls, err := d.finish(false)
	testutil.FailErr(t, "finish thought and tool", err)
	if visible != "Done." || len(calls) != 1 || calls[0].Name != "command" {
		t.Fatalf("answer = %q, calls = %+v", visible, calls)
	}
}

func TestThoughtChannelRejectsMalformedBoundaries(t *testing.T) {
	for _, part := range []struct {
		id   uint32
		text string
	}{
		{100, "<|channel>"}, {500, "analysis\n"}, {101, "<channel|>"}, {48, "<|tool_call>"},
	} {
		d := newOutputDecoder(modelcall.CompletionRequest{})
		start := uint32(100)
		_, err := d.step(&start, "<|channel>")
		testutil.FailErr(t, "start thought", err)
		if _, err := d.step(&part.id, part.text); err == nil {
			t.Fatalf("accepted invalid channel fragment: %d %q", part.id, part.text)
		}
	}
	d := newOutputDecoder(modelcall.CompletionRequest{})
	start := uint32(100)
	_, err := d.step(&start, "<|channel>")
	testutil.FailErr(t, "start unfinished thought", err)
	if _, err := d.finish(false); err == nil {
		t.Fatal("accepted unfinished thought as complete")
	}
	_, err = d.finish(true)
	testutil.FailErr(t, "preserve capped thought for truncation handling", err)
}
