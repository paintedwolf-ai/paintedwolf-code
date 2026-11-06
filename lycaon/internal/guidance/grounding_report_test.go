package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

func TestFormatOffenderReport_zero(t *testing.T) {
	r := guidance.FormatOffenderReport(nil)
	if r.Count != 0 || r.Sample != "" || r.Omitted != 0 {
		t.Fatalf("zero case = %+v", r)
	}
}

func TestFormatOffenderReport_single(t *testing.T) {
	r := guidance.FormatOffenderReport([]string{"internal/foo.go:1"})
	if r.Count != 1 || r.Sample != "internal/foo.go:1" || r.Omitted != 0 {
		t.Fatalf("single = %+v", r)
	}
}

func TestFormatOffenderReport_many(t *testing.T) {
	tokens := make([]string, 100)
	for i := range tokens {
		tokens[i] = "internal/file_" + strings.Repeat("x", 3) + ".go:" + string(rune('0'+i%10))
	}
	r := guidance.FormatOffenderReport(tokens)
	if r.Count != 100 {
		t.Fatalf("count = %d want 100", r.Count)
	}
	if r.Omitted != 92 {
		t.Fatalf("omitted = %d want 92", r.Omitted)
	}
	if guidance.MaxOffenderDisplay != 8 {
		t.Fatalf("MaxOffenderDisplay = %d", guidance.MaxOffenderDisplay)
	}
	if len(r.Sample) > guidance.MaxOffenderSampleChars {
		t.Fatalf("sample too long: %d", len(r.Sample))
	}
}

func TestGroundingHintData_ordersNamespacedSupersededAndGateHandles(t *testing.T) {
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "leg-a:read#1", Kind: "read", SourceTool: "read", SupersededBy: "leg-a:read#2"},
		{Handle: "leg-a:read#2", Kind: "read", SourceTool: "read"},
		{Handle: "leg-b:command#1", Kind: "command", SourceTool: "command"},
		{Handle: "dialed_host#a.example", Kind: "dialed_host"},
		{Handle: "dialed_host#b.example", Kind: "dialed_host"},
		{Handle: "gate#1", GateType: "review"},
	})
	data := guidance.GroundingHintData([]string{`leg-a:read#9:1 ("excerpt")`, "src/a.go:10"}, ev)
	sample, _ := data["observed_handles_sample"].(string)
	want := "leg-a:read#2, leg-a:read#1, leg-b:command#1, dialed_host#a.example, dialed_host#b.example, gate#1"
	if sample != want {
		t.Fatalf("sample = %q, want %q", sample, want)
	}
}
