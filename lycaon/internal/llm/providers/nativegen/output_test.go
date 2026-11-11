package nativegen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestToolOutputUsesTokenBoundariesAndPreservesLiteralBytes(t *testing.T) {
	decoder := newOutputDecoder(modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read_file"}}})
	step := func(token uint32, text string) string {
		t.Helper()
		visible, err := decoder.step(&token, text)
		if err != nil {
			testutil.FailErr(t, "decode output token", err)
		}
		return visible
	}
	literal := `C:\new\path <|tool_call> and <|"|> é`
	if step(500, "Literal <|tool_call> text") != "Literal <|tool_call> text" {
		t.Fatal("ordinary text became a control token")
	}
	step(48, "<|tool_call>")
	step(500, "call:read_file{path:")
	step(52, `<|"|>`)
	step(500, literal)
	step(52, `<|"|>`)
	step(500, ",nested:{flag:true,number:9007199254740993}}")
	step(49, "<tool_call|>")
	calls, err := decoder.finish(false)
	if err != nil || len(calls) != 1 || calls[0].ArgsMalformed || calls[0].Args["path"] != literal {
		t.Fatalf("decoded calls = %+v, %v", calls, err)
	}
	nested := calls[0].Args["nested"].(map[string]any)
	if nested["number"] != json.Number("9007199254740993") {
		t.Fatalf("lost integer precision: %+v", nested)
	}
}

func TestToolOutputMarksIncompleteAndMalformedArguments(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		decoder := newOutputDecoder(modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read_file"}}})
		start, ordinary, end := uint32(48), uint32(500), uint32(49)
		if _, err := decoder.step(&start, "<|tool_call>"); err != nil {
			testutil.FailErr(t, "start tool call", err)
		}
		if _, err := decoder.step(&ordinary, "call:read_file{path:"); err != nil {
			testutil.FailErr(t, "decode tool arguments", err)
		}
		if !truncated {
			if _, err := decoder.step(&end, "<tool_call|>"); err != nil {
				testutil.FailErr(t, "end tool call", err)
			}
		}
		calls, err := decoder.finish(truncated)
		if err != nil || len(calls) != 1 || calls[0].ArgsTruncated != truncated || calls[0].ArgsMalformed == truncated || calls[0].Args != nil {
			t.Fatalf("invalid arguments became executable: %+v, %v", calls, err)
		}
	}
}

func TestToolOutputRefusesUnofferedCallsAndParserAmbiguity(t *testing.T) {
	decoder := newOutputDecoder(modelcall.CompletionRequest{})
	start := uint32(48)
	if _, err := decoder.step(&start, "<|tool_call>"); err == nil {
		t.Fatal("accepted a call without offered tools")
	}
	for _, text := range []string{
		`{a:1,a:2}`, `{a:01}`, `{a:1e999}`, `{a:1,}`, `{a:[1,]}`, `{a:true} trailing`,
		strings.Repeat("{a:", 65) + "1" + strings.Repeat("}", 65),
	} {
		if _, err := parseArguments(text); err == nil {
			t.Fatalf("accepted invalid arguments: %s", text)
		}
	}
}
