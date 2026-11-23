package textsearch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// generatedLine builds one line of catalog-like JSON of exactly n bytes.
func generatedLine(n int) []byte {
	const chunk = `{\"id\":\"regex_pattern_bytes\",\"maximum\":65536,\"unit\":\"bytes\"},`
	return []byte(strings.Repeat(chunk, n/len(chunk)+1)[:n])
}

func TestOriginalGrepCompletesOnAGeneratedLine(t *testing.T) {
	ctx := t.Context()
	m, err := Compile(ctx, Spec{Patterns: []string{`"wait"|toolWait|WaitTool|wait_for|sleep_for`}, Dialect: Extended, IgnoreCase: true}, catalogLimits(t))
	testutil.FailErr(t, "compile text-search pattern", err)
	line := generatedLine(1_428_296)
	if _, ok, err := m.Select(ctx, line); err != nil || ok {
		t.Fatalf("generated line without a match: %v %v", ok, err)
	}
	copy(line[len(line)-len("Sleep_For"):], "Sleep_For")
	if _, ok, err := m.Select(ctx, line); err != nil || !ok {
		t.Fatalf("generated line with a match at its end: %v %v", ok, err)
	}
}

func TestLiteralRecordsAreBoundedByWorkNotDecodeMemory(t *testing.T) {
	ctx := t.Context()
	limits := catalogLimits(t)
	// (len+1) x len(pattern) is exactly the work ceiling for an 8-byte pattern.
	record := bytes.Repeat([]byte("x"), int(limits.WorkSteps/8)-1)
	if int64(len(record))*12+8 <= limits.MemoryBytes {
		t.Fatalf("record of %d bytes is within the decode ceiling", len(record))
	}
	eight, err := Compile(ctx, Spec{Patterns: []string{"abcdefgh"}, Dialect: Extended}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	if _, ok, err := eight.Select(ctx, record); err != nil || ok {
		t.Fatalf("8-byte literal at the work ceiling: %v %v", ok, err)
	}
	nine, err := Compile(ctx, Spec{Patterns: []string{"abcdefghi"}, Dialect: Extended}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	if _, _, err := nine.Select(ctx, record); budgetResource(t, err) != "regex_work_steps" {
		t.Fatalf("9-byte literal over the work ceiling: %v", err)
	}
}

func TestVMRecordsStopAtTheDecodeCeiling(t *testing.T) {
	ctx := t.Context()
	limits := catalogLimits(t)
	m, err := Compile(ctx, Spec{Patterns: []string{`^[0-9]`}, Dialect: Extended}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	// The session's own runner state shares the ledger with the decode.
	ceiling := int((limits.MemoryBytes - 8) / 12)
	record := bytes.Repeat([]byte("a"), ceiling+1)
	if _, ok, err := m.Select(ctx, record[:ceiling-1024]); err != nil || ok {
		t.Fatalf("record below the decode ceiling: %v %v", ok, err)
	}
	if _, _, err := m.Select(ctx, record); budgetResource(t, err) != "evaluation_memory_bytes" {
		t.Fatalf("record over the decode ceiling: %v", err)
	}
}
