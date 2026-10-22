//go:build darwin || linux

package textutil

import "testing"

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
