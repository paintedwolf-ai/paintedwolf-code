package nativegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOutputContractCarriesExactOfferedNames(t *testing.T) {
	req := modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}, {Name: "namespace tool"}}}
	contract, err := requestOutputContract(req)
	testutil.FailErr(t, "prepare contract", err)
	encoded, err := json.Marshal(contract)
	testutil.FailErr(t, "encode contract", err)
	if string(encoded) != `{"allowed_tools":["read","namespace tool"]}` {
		t.Fatalf("contract = %s", encoded)
	}
	contract, err = requestOutputContract(modelcall.CompletionRequest{})
	testutil.FailErr(t, "prepare empty contract", err)
	encoded, err = json.Marshal(contract)
	testutil.FailErr(t, "encode empty contract", err)
	if string(encoded) != `{"allowed_tools":[]}` {
		t.Fatalf("empty contract = %s", encoded)
	}
}

func TestOutputContractRejectsAmbiguousOrUnboundedNames(t *testing.T) {
	for _, names := range [][]string{{""}, {" read"}, {"read "}, {"read{"}, {"re\nad"}, {"re\rad"}, {"\xff"}, {"read", "read"}, {strings.Repeat("x", 257)}} {
		req := modelcall.CompletionRequest{}
		for _, name := range names {
			req.Tools = append(req.Tools, tools.ToolMeta{Name: name})
		}
		if _, err := requestOutputContract(req); err == nil {
			t.Errorf("accepted names %q", names)
		}
	}
}

func TestOutputProtocolFailureKeepsOnlyBoundedControls(t *testing.T) {
	d := newOutputDecoder(modelcall.CompletionRequest{})
	d.provider, d.model = "nativegen", "model"
	ordinary := uint32(9000)
	_, err := d.step(&ordinary, "private text")
	testutil.FailErr(t, "decode ordinary text", err)
	for i := 0; i < 20; i++ {
		token := uint32(52)
		d.observe(&token)
	}
	bad := uint32(101)
	_, err = d.step(&bad, "<channel|>")
	var failure *OutputProtocolError
	if !errors.As(err, &failure) || failure.Reason != "unexpected_control" || failure.Phase != "text" || failure.Position != 22 || len(failure.Controls) != 16 || failure.Controls[15].ID != 101 || failure.Controls[15].Position != 22 || failure.ProviderID != "nativegen" || failure.Model != "model" {
		t.Fatalf("failure = %#v, %v", failure, err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "9000") {
		t.Fatal("diagnostic exposed ordinary output")
	}
}

func TestCappedPartialToolHeadersNeverInventCalls(t *testing.T) {
	for _, header := range []string{"", "c", "call:", "call:rea", "call:read"} {
		d := newOutputDecoder(modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}}})
		open, ordinary := uint32(48), uint32(500)
		_, err := d.step(&open, "<|tool_call>")
		testutil.FailErr(t, "open call", err)
		_, err = d.step(&ordinary, header)
		testutil.FailErr(t, "partial header", err)
		calls, err := d.finish(true)
		testutil.FailErr(t, "finish capped header", err)
		if len(calls) != 0 {
			t.Fatalf("invented call for %q", header)
		}
	}
}

func TestStructuredOutputSuppressesToolsAndThought(t *testing.T) {
	req := modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}}, ResponseFormat: &modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONObject}}
	contract, err := requestOutputContract(req)
	testutil.FailErr(t, "prepare schema contract", err)
	if len(contract.AllowedTools) != 0 {
		t.Fatal("schema offered tools")
	}
	for _, token := range []uint32{48, 100} {
		d := newOutputDecoder(req)
		if _, err := d.step(&token, "<|channel>"); err == nil {
			t.Fatal("schema accepted envelope")
		}
	}
}

func TestCappedMalformedHeaderRemainsFailure(t *testing.T) {
	d := newOutputDecoder(modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}}})
	d.inCall = true
	d.body.WriteString("call:invented")
	if _, err := d.finish(true); err == nil {
		t.Fatal("malformed header became truncation")
	}
}

func TestOutputContractLimitsAndForbiddenTools(t *testing.T) {
	req := modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}}, ToolUse: modelcall.ToolUseForbidden}
	contract, err := requestOutputContract(req)
	testutil.FailErr(t, "forbidden contract", err)
	if len(contract.AllowedTools) != 0 {
		t.Fatal("forbidden request offered tools")
	}
	for _, size := range []int{8, 256} {
		req = modelcall.CompletionRequest{}
		for i := 0; i < 513; i++ {
			name := fmt.Sprintf("%04d", i) + strings.Repeat("x", size-4)
			req.Tools = append(req.Tools, tools.ToolMeta{Name: name})
		}
		if _, err := requestOutputContract(req); err == nil {
			t.Fatalf("accepted overbound size %d", size)
		}
	}
}

func TestOutputProtocolKindsAreIndependentOfToolAvailability(t *testing.T) {
	for _, callable := range []bool{false, true} {
		req := modelcall.CompletionRequest{}
		if callable {
			req.Tools = []tools.ToolMeta{{Name: "read"}}
		}
		d := newOutputDecoder(req)
		for reason, kind := range map[string]failure.OutputProtocolFailureKind{"unexpected_control": failure.MalformedEnvelope, "decoding_mismatch": failure.DecodeMismatch, "output_bound": failure.OutputBound} {
			var protocol *failure.ProviderOutputProtocolError
			if !errors.As(d.protocolError(reason), &protocol) || protocol.Kind != kind {
				t.Fatalf("kind for %s = %+v", reason, protocol)
			}
		}
	}
}

func TestQuoteControlRemainsLiteralOutsideCalls(t *testing.T) {
	d := newOutputDecoder(modelcall.CompletionRequest{})
	token := uint32(52)
	text, err := d.step(&token, `<|"|>`)
	testutil.FailErr(t, "literal quote control", err)
	if text != `<|"|>` {
		t.Fatalf("literal = %q", text)
	}
}
