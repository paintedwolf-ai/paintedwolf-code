//go:build darwin || linux

package textutil

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"
	"testing"
)

func TestPrintfPaddingUsesBoundedWorkingMemory(t *testing.T) {
	h := newHarness(t)
	r := h.run("printf '%16777216s' x", "")
	if r.err != nil || r.result.Status != 0 || len(r.stdout) != 16777216 || r.stdout[0] != ' ' || r.stdout[len(r.stdout)-1] != 'x' {
		t.Fatalf("padding result status=%d bytes=%d error=%v", r.result.Status, len(r.stdout), r.err)
	}
	if peak := h.memory.Peak().Working; peak > 1<<20 {
		t.Fatalf("padding retained %d working bytes", peak)
	}
}

func TestPrintfNumericBoundsPreserveEarlierOutput(t *testing.T) {
	for _, format := range []string{"%.100000000d", "%.100000000f", "%.999999999999999999999999999999999d"} {
		t.Run(format, func(t *testing.T) {
			h := newHarness(t)
			r := h.run("printf 'prefix"+format+"' 1", "")
			reject := r.expectRefusal(t, "numeric conversion bound", "COMMAND_LIMIT_EXCEEDED")
			if r.stdout != "prefix" || reject.Data["limit_id"] != "record_bytes" {
				t.Fatalf("partial output %q, refusal %+v", r.stdout, reject.Data)
			}
			if peak := h.memory.Peak().Working; peak > 1<<20 {
				t.Fatalf("unbounded conversion admitted %d bytes", peak)
			}
		})
	}
}

func TestPrintfWideNumericPadding(t *testing.T) {
	for _, format := range []string{"%16777216d", "%016777216d", "%16777216.2f", "%#016777216x", "%-16777216d"} {
		t.Run(format, func(t *testing.T) {
			h := newHarness(t)
			r := h.run("printf '"+format+"' 12", "")
			if r.err != nil || r.result.Status != 0 || len(r.stdout) != 16777216 {
				t.Fatalf("wide numeric status=%d bytes=%d error=%v", r.result.Status, len(r.stdout), r.err)
			}
			if peak := h.memory.Peak().Working; peak > 1<<20 {
				t.Fatalf("wide number retained %d bytes", peak)
			}
			switch format {
			case "%016777216d":
				if r.stdout[0] != '0' || r.stdout[len(r.stdout)-2:] != "12" {
					t.Fatal("zero padding changed digits")
				}
			case "%#016777216x":
				if r.stdout[:3] != "0x0" || r.stdout[len(r.stdout)-1] != 'c' {
					t.Fatal("zero padding changed base prefix")
				}
			case "%-16777216d":
				if r.stdout[:2] != "12" || r.stdout[len(r.stdout)-1] != ' ' {
					t.Fatal("left padding changed")
				}
			}
		})
	}
}

func TestPrintfFormatAdmissionPrecedesParsing(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1024, State: 1024})
	testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
	// An invalid final conversion would require parsing the entire prefix. With
	// no admission the parser must never reach it or allocate its directives.
	text := strings.Repeat("%s", 8192) + "%q"
	allocations := testing.AllocsPerRun(20, func() {
		format, release, err := parseFormat(context.Background(), memory, text)
		var refused *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &refused) || format != nil || release != nil {
			t.Fatalf("parse before admission: %v", err)
		}
	})
	if allocations > 4 || memory.Peak().Working != 0 || memory.Reservations() != 0 {
		t.Fatalf("refused parser allocated %.0f objects, peak=%d reservations=%d", allocations, memory.Peak().Working, memory.Reservations())
	}
}

func TestPrintfFormatReleasesParserGrowthAtRepresentationBoundary(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 8 << 20, State: 1024})
	testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
	format, release, err := parseFormat(t.Context(), memory, strings.Repeat("%s", 8192))
	testutil.FailErr(t, "parseFormat failed", err)
	if len(format) != 8192 || memory.InUse().Working <= 0 || memory.InUse().Working >= memory.Peak().Working {
		t.Fatalf("plan len=%d held=%d peak=%d", len(format), memory.InUse().Working, memory.Peak().Working)
	}
	release()
	if memory.InUse().Working != 0 {
		t.Fatal("parsed format retained its reservation")
	}
}
