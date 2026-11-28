package evidence_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

type gb03V02Case struct {
	BaseSeed        string   `json:"base_seed"`
	InstanceIndex   int      `json:"instance_index"`
	Payload         string   `json:"payload"`
	ImitatedMarkers []string `json:"imitated_markers"`
}

type gb03V02Result struct {
	BaseSeed       string   `json:"base_seed"`
	InstanceIndex  int      `json:"instance_index"`
	Passed         bool     `json:"passed"`
	Kind           string   `json:"kind"`
	Shape          string   `json:"shape"`
	Path           string   `json:"path,omitempty"`
	IndexedPaths   []string `json:"indexed_paths,omitempty"`
	LineRangeCount int      `json:"line_range_count"`
	HasSourceDeliv bool     `json:"has_source_delivery"`
	SpoofVerdicts  []string `json:"spoof_verdicts,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

// TestGB03V02InProcessLookalikes executes the GB03 V02 generated lookalike corpus
// against D1's real evidence.BuildEvidenceRecordRooted and evidence.Resolve via a
// test-only Go overlay (zero edits to any shipped D1 file).
func TestGB03V02InProcessLookalikes(t *testing.T) {
	casesPath := strings.TrimSpace(os.Getenv("GND_GB03_V02_CASES_JSONL"))
	resultsPath := strings.TrimSpace(os.Getenv("GND_GB03_V02_RESULTS_JSONL"))
	if casesPath == "" || resultsPath == "" {
		t.Skip("GND_GB03_V02_CASES_JSONL and GND_GB03_V02_RESULTS_JSONL not set")
	}

	inFile, err := os.Open(casesPath)
	if err != nil {
		t.Fatalf("open cases: %v", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(resultsPath)
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer outFile.Close()
	outBuf := bufio.NewWriter(outFile)
	defer outBuf.Flush()

	roots := evidence.CitationRoots{ProjectDir: "/tmp/gb03-v02-inprocess"}
	scanner := bufio.NewScanner(inFile)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	total := 0
	failures := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c gb03V02Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("unmarshal case: %v", err)
		}
		total++

		rec := evidence.BuildEvidenceRecordRooted(
			roots,
			"command",
			map[string]any{"command": "cat lookalike.txt"},
			c.Payload,
		)
		indexed := evidence.IndexedPathsForRecord(rec)

		// Build a 1-record ledger with only command#1 and verify spoofed coordinates
		// never resolve as grounded.
		ev := evidence.Ledger{
			Handles: map[string]evidence.Record{"command#1": rec},
			ByPath:  map[string][]string{},
		}
		for _, p := range indexed {
			ev.ByPath[p] = append(ev.ByPath[p], "command#1")
		}

		spoofTriples := []struct {
			triple evidence.Triple
			handle string
		}{
			{evidence.Triple{Path: "internal/fake_0.go", Line: 1}, "read#1"},
			{evidence.Triple{Path: "src/spoof_0.go", Line: 1}, "read#2"},
			{evidence.Triple{Path: "a.go", Line: 5}, "grep#1"},
			{evidence.Triple{Path: "internal/fake_0.go", Line: 1}, ""},
		}
		var spoofVerdicts []string
		spoofGrounded := false
		for _, st := range spoofTriples {
			res := evidence.Resolve(roots, st.triple, ev, st.handle)
			spoofVerdicts = append(spoofVerdicts, string(res.Verdict))
			if res.Verdict.Grounded() {
				spoofGrounded = true
			}
		}

		var reasons []string
		if rec.SourceDelivery != nil {
			reasons = append(reasons, "unexpected_source_delivery")
		}
		if len(rec.LineRanges) > 0 {
			reasons = append(reasons, "unexpected_line_ranges")
		}
		if rec.Path != "" {
			reasons = append(reasons, "unexpected_record_path")
		}
		if len(indexed) > 0 {
			reasons = append(reasons, "unexpected_indexed_paths")
		}
		if spoofGrounded {
			reasons = append(reasons, "spoofed_coordinate_grounded")
		}

		passed := len(reasons) == 0
		if !passed {
			failures++
		}
		res := gb03V02Result{
			BaseSeed:       c.BaseSeed,
			InstanceIndex:  c.InstanceIndex,
			Passed:         passed,
			Kind:           rec.Kind,
			Shape:          string(rec.Shape),
			Path:           rec.Path,
			IndexedPaths:   indexed,
			LineRangeCount: len(rec.LineRanges),
			HasSourceDeliv: rec.SourceDelivery != nil,
			SpoofVerdicts:  spoofVerdicts,
			Reason:         strings.Join(reasons, ","),
		}
		enc, _ := json.Marshal(res)
		outBuf.Write(enc)
		outBuf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan cases: %v", err)
	}
	if err := outBuf.Flush(); err != nil {
		t.Fatalf("flush results: %v", err)
	}
	t.Logf("GB03 V02 in-process complete: total=%d failures=%d", total, failures)
}
