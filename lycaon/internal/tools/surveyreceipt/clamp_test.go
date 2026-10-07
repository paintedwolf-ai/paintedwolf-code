package surveyreceipt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClampSessionToolOutputFindResults(t *testing.T) {
	results := make([]map[string]any, 0, 50)
	for i := 0; i < 120; i++ {
		results = append(results, map[string]any{
			"path": strings.Repeat("x", 120) + "/file.go",
			"type": "file",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"results":     results,
		"offset":      500,
		"truncated":   false,
		"next_offset": 550,
		"receipt":     New("find", "lycaon-den", 120, 0, false),
	})
	testutil.FailErr(t, "json.Marshal failed", err)
	clamp, ok := ClampSessionToolOutput(string(payload), 8192)
	if !ok {
		t.Fatalf("expected clamp payload_len=%d", len(payload))
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(clamp.Output), &obj); err != nil {
		t.Fatalf("invalid json: %v body=%q", err, clamp.Output)
	}
	page, _ := obj["results"].([]any)
	if len(page) == 0 || len(page) >= 120 {
		t.Fatalf("results len = %d want between 1 and 119", len(page))
	}
	if obj["truncated"] != true {
		t.Fatalf("truncated = %v", obj["truncated"])
	}
	if jsonvalue.Int(obj["next_offset"]) != 500+len(page) {
		t.Fatalf("next_offset = %v", obj["next_offset"])
	}
	if clamp.Vars["page_key"] != "results" {
		t.Fatalf("page_key = %v", clamp.Vars["page_key"])
	}
	if jsonvalue.Int(clamp.Vars["kept"]) != len(page) {
		t.Fatalf("kept = %v want %d", clamp.Vars["kept"], len(page))
	}
}

func TestClampSessionToolOutputReadContent(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("a", 80)
	}
	content := hostmarker.FormatNumberedLines(lines, 1)
	payload, err := json.Marshal(map[string]any{
		"path":        "go.sum",
		"content":     content,
		"total_lines": 100,
		"offset":      1,
		"limit":       2000,
		"end_line":    100,
		"truncated":   false,
		"receipt":     New("read", "go.sum", 1, len(content), false),
	})
	testutil.FailErr(t, "json.Marshal failed", err)
	clamp, ok := ClampSessionToolOutput(string(payload), 4096)
	if !ok {
		t.Fatal("expected read clamp")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(clamp.Output), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if obj["truncated"] != true {
		t.Fatalf("truncated = %v want true", obj["truncated"])
	}
	trimmed, _ := obj["content"].(string)
	if len(trimmed) >= len(content) {
		t.Fatalf("content not trimmed: len=%d", len(trimmed))
	}
	if jsonvalue.Int(obj["end_line"]) <= 0 {
		t.Fatalf("end_line = %v", obj["end_line"])
	}
	if clamp.Vars["page_key"] != "content" {
		t.Fatalf("page_key = %v", clamp.Vars["page_key"])
	}
	if jsonvalue.Int(clamp.Vars["kept"]) != len(trimmed) {
		t.Fatalf("kept = %v want %d", clamp.Vars["kept"], len(trimmed))
	}
}

func TestClampSessionToolOutputReadContentLineAware(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = strings.Repeat("y", 80)
	}
	content := hostmarker.FormatNumberedLines(lines, 1)
	payload, err := json.Marshal(map[string]any{
		"path":        "big.go",
		"content":     content,
		"total_lines": 40,
		"offset":      1,
		"limit":       40,
		"end_line":    40,
		"truncated":   false,
		"receipt":     New("read", "big.go", 1, len(content), false),
	})
	testutil.FailErr(t, "json.Marshal failed", err)
	clamp, ok := ClampSessionToolOutput(string(payload), 1200)
	if !ok {
		t.Fatal("expected read clamp")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(clamp.Output), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	trimmed, _ := obj["content"].(string)
	lastLine := trimmed[strings.LastIndex(trimmed, "\n")+1:]
	lastNumber, lastText, numbered := hostmarker.ParseNumberedLine(lastLine)
	if !numbered || lastText != strings.Repeat("y", 80) {
		t.Fatalf("last line not a complete numbered row: %q", lastLine)
	}
	if jsonvalue.Int(obj["end_line"]) != lastNumber {
		t.Fatalf("end_line = %v, want last kept line %d", obj["end_line"], lastNumber)
	}
	if jsonvalue.Int(obj["next_offset"]) <= jsonvalue.Int(obj["end_line"]) {
		t.Fatalf("next_offset = %v end_line = %v", obj["next_offset"], obj["end_line"])
	}
}

func TestClampSessionToolOutputNoOpWhenSmall(t *testing.T) {
	in := `{"results":[],"offset":0,"receipt":{"tool":"find","paths_touched":0,"bytes_returned":10,"truncated":false,"scope_hash":"open"}}`
	clamp, ok := ClampSessionToolOutput(in, 65536)
	if ok || clamp.Output != in {
		t.Fatalf("ClampSessionToolOutput() = (%+v, %v)", clamp, ok)
	}
}

func TestSessionClampAdmitsFinalReceiptAndPreservesContinuation(t *testing.T) {
	entries := []any{strings.Repeat("a", 130), strings.Repeat("b", 190), strings.Repeat("c", 250)}
	payload, err := json.Marshal(map[string]any{
		"offset": 7, "results": entries,
		"receipt": map[string]any{"bytes_returned": 0, "truncated": false},
	})
	testutil.FailErr(t, "marshal survey receipt", err)
	applied := 0
	for budget := 100; budget < len(payload); budget++ {
		clamp, ok := ClampSessionToolOutput(string(payload), budget)
		if !ok {
			continue
		}
		applied++
		if len(clamp.Output) > budget {
			t.Fatalf("final metadata escaped byte cap: budget=%d actual=%d", budget, len(clamp.Output))
		}
		var got map[string]any
		testutil.FailErr(t, "decode clamped receipt", json.Unmarshal([]byte(clamp.Output), &got))
		page := got["results"].([]any)
		if got["receipt"].(map[string]any)["bytes_returned"] != float64(len(clamp.Output)) {
			t.Fatalf("receipt does not measure final bytes: %+v", got)
		}
		if jsonvalue.Int(got["next_offset"]) != 7+len(page) {
			t.Fatalf("continuation skips retained boundary: %+v", got)
		}
		for i := range page {
			if page[i] != entries[i] {
				t.Fatalf("clamp altered retained entry %d", i)
			}
		}
		if len(page) == 0 && jsonvalue.Int(clamp.Vars["next_offset"]) != 0 {
			t.Fatalf("empty page advertised progress: %+v", clamp.Vars)
		}
	}
	if applied == 0 {
		t.Fatal("fixture never exercised an admitted projection")
	}
}

func TestSessionClampUnnumberedContentDoesNotInventLineCoverage(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"content": strings.Repeat(strings.Repeat("x", 60)+"\n", 30),
		"offset":  20, "end_line": 49, "next_offset": 50,
	})
	testutil.FailErr(t, "marshal unnumbered content", err)
	clamp, ok := ClampSessionToolOutput(string(payload), 600)
	if !ok {
		t.Fatal("fixture did not exercise content projection")
	}
	var got map[string]any
	testutil.FailErr(t, "decode projected content", json.Unmarshal([]byte(clamp.Output), &got))
	if _, present := got["end_line"]; present {
		t.Fatalf("retained original end_line after removing content: %+v", got)
	}
	if _, present := got["next_offset"]; present {
		t.Fatalf("invented continuation without numbered lines: %+v", got)
	}
	if clamp.KeptThroughLine != 0 || jsonvalue.Int(clamp.Vars["next_offset"]) != 0 {
		t.Fatalf("invented line coverage or continuation: %+v", clamp)
	}
}
