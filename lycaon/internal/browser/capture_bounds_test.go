package browser

import (
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/browserengine"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPageEvidenceBoundsUntrustedConsoleOutput(t *testing.T) {
	ev := &pageEvidence{screen: func(line string) (string, error) { return line, nil }}
	line := strings.Repeat("界", MaxConsoleLogLineBytes)
	for range MaxConsoleLogEntries + 10 {
		ev.appendConsole(line)
	}
	report := ev.since(evidenceMark{})
	logs, truncated := report.Log, report.LogTruncated
	if !truncated {
		t.Fatal("console evidence did not report truncation")
	}
	total := 0
	for _, got := range logs {
		if len(got) > MaxConsoleLogLineBytes || !utf8.ValidString(got) {
			t.Fatalf("invalid bounded log line: bytes=%d utf8=%v", len(got), utf8.ValidString(got))
		}
		total += len(got)
	}
	if len(logs) > MaxConsoleLogEntries || total > MaxConsoleLogBytes {
		t.Fatalf("log bounds entries=%d bytes=%d", len(logs), total)
	}
}

func TestPageEvidenceKeepsTheNewestConsoleLinesAndReportsEachDriveDelta(t *testing.T) {
	ev := &pageEvidence{screen: func(line string) (string, error) { return line, nil }}
	for i := range MaxConsoleLogEntries + 5 {
		ev.appendConsole(fmt.Sprintf("line %d", i))
	}
	all := ev.since(evidenceMark{}).Log
	if len(all) != MaxConsoleLogEntries || all[len(all)-1] != fmt.Sprintf("line %d", MaxConsoleLogEntries+4) || all[0] != "line 5" {
		t.Fatalf("retained console = first %q last %q (%d lines), want the newest %d", all[0], all[len(all)-1], len(all), MaxConsoleLogEntries)
	}
	mark := ev.mark()
	ev.appendConsole("during drive")
	delta := ev.since(mark)
	if len(delta.Log) != 1 || delta.Log[0] != "during drive" {
		t.Fatalf("drive delta = %q, want only the line written after the mark", delta.Log)
	}
	if delta.LogTruncated {
		t.Fatal("a complete drive delta reported truncation from the page's earlier history")
	}
	ev.appendConsole(strings.Repeat("y", MaxConsoleLogLineBytes+1))
	if !ev.since(mark).LogTruncated {
		t.Fatal("a line cut inside the delta did not report truncation")
	}
	later := ev.mark()
	for i := range MaxConsoleLogEntries + 1 {
		ev.appendConsole(fmt.Sprintf("flood %d", i))
	}
	if !ev.since(later).LogTruncated {
		t.Fatal("lines dropped from inside the delta did not report truncation")
	}
}

func TestMarshalDriverResultRejectsOversizedSemanticEvidence(t *testing.T) {
	_, err := marshalDriverResult("snapshot", strings.Repeat("x", MaxSemanticOutputBytes+1))
	var rejected *browserengine.RejectError
	if !errors.As(err, &rejected) || rejected.Code != "CAPTURE_OUTPUT_OVERSIZED" {
		t.Fatalf("error = %v, want CAPTURE_OUTPUT_OVERSIZED", err)
	}
	if rejected.Data["capture_semantic_output"] != true || rejected.Data["capture_output_kind"] != "semantic snapshot" {
		t.Fatalf("semantic failure lost its output channel: %+v", rejected.Data)
	}
	if rejected.Data["bytes"].(int) <= rejected.Data["max_bytes"].(int) {
		t.Fatalf("oversize refusal has no measured excess: %+v", rejected.Data)
	}
}
