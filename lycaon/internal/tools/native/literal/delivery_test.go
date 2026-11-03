package literal

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const deliveredSource = "package fixture\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n"

// TestDeliveredCommandWindowsQualifyOnlyTheirExactRanges carries cat and
// tail output through mapping projection into the delivery owner: a whole
// delivered window and an exactly mapped page qualify their own lines, while
// an unseen remainder and transformed bytes qualify nothing.
func TestDeliveredCommandWindowsQualifyOnlyTheirExactRanges(t *testing.T) {
	h := newHarness(t, map[string]string{"fixture.go": deliveredSource})
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: 1 << 20})
	testutil.FailErr(t, "program memory", err)
	confirm := func(r run, start, length int64) (evidence.Record, error) {
		t.Helper()
		lease, err := commandprogram.ProjectStreamMappings(t.Context(), memory, r.mappings, start, length, commandprogram.MappingExactSlice)
		testutil.FailErr(t, "project page", err)
		defer lease.Close()
		if len(lease.Mappings) != 1 {
			t.Fatalf("page mappings = %+v", lease.Mappings)
		}
		pin, _ := h.pins.pins[r.execution][0].SourcePin()
		observed, err := evidence.ConfirmDeliveredSource("fixture.go", pin, []byte(deliveredSource), []byte(r.stdout[start:start+length]), lease.Mappings[0], "response-1", "delivery-1")
		return observed.Record(), err
	}

	whole := h.exec("", "cat", "fixture.go")
	record, err := confirm(whole, 0, int64(len(whole.stdout)))
	testutil.FailErr(t, "confirm whole delivered cat", err)
	if got := record.LineRanges; len(got) != 1 || got[0].Start != 1 || got[0].End != 5 || record.SourceDelivery == nil {
		t.Fatalf("whole delivery = %+v", record)
	}

	// Only the delivered page of retained output qualifies.
	pageStart := int64(strings.Index(whole.stdout, "func One"))
	pageLength := int64(len("func One() int { return 1 }\n"))
	page, err := confirm(whole, pageStart, pageLength)
	testutil.FailErr(t, "confirm delivered page", err)
	if got := page.LineRanges; len(got) != 1 || got[0].Start != 3 || got[0].End != 3 {
		t.Fatalf("page delivery = %+v", page.LineRanges)
	}

	tail := h.exec("", "tail", "-n", "1", "fixture.go")
	tailed, err := confirm(tail, 0, int64(len(tail.stdout)))
	testutil.FailErr(t, "confirm tail window", err)
	if got := tailed.LineRanges; len(got) != 1 || got[0].Start != 5 || got[0].End != 5 {
		t.Fatalf("tail delivery = %+v", tailed.LineRanges)
	}

	// Transformed output carries no mapping, and its bytes cannot borrow one.
	shown := h.exec("", "cat", "-A", "fixture.go")
	if len(shown.mappings) != 0 {
		t.Fatalf("notation output mapped: %+v", shown.mappings)
	}
	pin, _ := h.pins.pins[whole.execution][0].SourcePin()
	borrowed := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: 0, SourceEndByte: int64(len(shown.stdout[:16])), OutputEndByte: 16}
	if _, err := evidence.ConfirmDeliveredSource("fixture.go", pin, []byte(deliveredSource), []byte(shown.stdout[:16]), borrowed, "response-2", "delivery-2"); err == nil {
		t.Fatal("transformed bytes qualified as a delivered source window")
	}
	// A projection outside every literal window yields nothing to deliver.
	numbered := h.exec("", "cat", "-n", "fixture.go")
	lease, err := commandprogram.ProjectStreamMappings(t.Context(), memory, numbered.mappings, 0, 7, commandprogram.MappingExactSlice)
	testutil.FailErr(t, "project number prefix", err)
	defer lease.Close()
	if len(lease.Mappings) != 0 {
		t.Fatalf("line-number prefix mapped: %+v", lease.Mappings)
	}
}
