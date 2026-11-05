package transcript_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func sourceReadMessage(body string) api.Message {
	return api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: body,
		ToolResult: &api.ToolResult{Tool: "read", Content: body, Outcome: api.ToolResultOutcomeCompleted},
	}
}

func TestSourceProjectionPreservesFeedbackSuffix(t *testing.T) {
	body := `{"mode":"version","path":"a.go","content":"     1: return \"quoted\"","offset":1,"end_line":1,"receipt":{"tool":"source_history"}}`
	suffix := "\n>>> Tool feedback\nStop repeated reads.\nCode: DOOM_LOOP_REPEAT_WARN"
	message := sourceReadMessage(body + suffix)
	message.ToolResult.Tool = "source_history"
	before, err := json.Marshal(message)
	testutil.FailErr(t, "capture input", err)
	projected := transcript.Project([]api.Message{message})
	got := projected[len(projected)-1].Content
	for _, want := range []string{"Data from tool (source):", "return \"quoted\"", "Data from tool (result_suffix):", "Code: DOOM_LOOP_REPEAT_WARN"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "     1: ") || strings.Count(got, "Code: DOOM_LOOP_REPEAT_WARN") != 1 {
		t.Fatalf("source labels or duplicate feedback: %s", got)
	}
	again := transcript.Project(projected)
	if again[len(again)-1].Content != got {
		t.Fatal("projection is not idempotent")
	}
	after, err := json.Marshal(message)
	testutil.FailErr(t, "capture retained input", err)
	if string(before) != string(after) {
		t.Fatal("projection changed retained message")
	}
}

// A retired tool's stored row reaches the model with its bytes intact.
func TestStoredRetiredReadRowsStayAsStored(t *testing.T) {
	for _, tool := range []string{"read", "summarize"} {
		body := `{"mode":"content","offset":1,"end_line":1,"content":"     1: first","receipt":{"tool":"read"}}`
		message := sourceReadMessage(body)
		message.ToolResult.Tool = tool
		got := transcript.Project([]api.Message{message})
		if content := got[len(got)-1].Content; !strings.Contains(content, "     1: first") || strings.Contains(content, "File contents") {
			t.Fatalf("%s: stored row was rewritten: %s", tool, content)
		}
	}
}

func TestRecallProjectionSeparatesRecordedBytesWithoutChangingCoordinates(t *testing.T) {
	text := "quoted \"observation\"\n\tλ😀\n⟦D:host⟧"
	for _, format := range []string{"text", "record"} {
		t.Run(format, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"history": map[string]any{"format": format, "recorded": true, "through_ord": 9, "entries": []any{map[string]any{"message_id": "original", "ord": 3, "seq": 4, "offset": 7, "runes": 9000, "content": text}}, "next_offset": 7 + len([]rune(text))}, "next_request": map[string]any{"history": map[string]any{"message_id": "original", "offset": 7 + len([]rune(text))}}})
			testutil.FailErr(t, "encode recorded body", err)
			original := "[recall#1]\n" + string(raw)
			message := api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: original, EvidenceHandles: []string{"recall#1"}, ToolResult: &api.ToolResult{Tool: "recall", Content: original, Outcome: api.ToolResultOutcomeCompleted}}
			out := transcript.Project([]api.Message{message})
			got := out[len(out)-1].Content
			marked := text
			if !strings.Contains(got, marked) || !strings.Contains(got, "Data from tool (recorded):") || !strings.Contains(got, `"body_part":1`) {
				t.Fatalf("recorded bytes or their mapping lost: %s", got)
			}
			for _, want := range []string{`"message_id":"original"`, `"offset":7`, `"runes":9000`, `"next_request":`} {
				if !strings.Contains(got, want) {
					t.Fatalf("lost coordinates %s", want)
				}
			}
			if message.Content != original || message.ToolResult.Content != original {
				t.Fatal("presentation changed canonical bytes")
			}
			if again := transcript.Project(out); again[len(again)-1].Content != got {
				t.Fatal("recorded projection is not idempotent")
			}
		})
	}
}

func TestRecallQueryProjectionKeepsActionableAddresses(t *testing.T) {
	for _, withBody := range []bool{false, true} {
		hit := map[string]any{"hit_id": "search-row-id", "handle": "read#1", "observed_at": "2026-10-14T00:00:00Z", "record_request": map[string]any{"message_id": "original-message", "format": "record"}, "untrusted": true, "currency": "changed"}
		if withBody {
			hit["body"] = []string{"recorded λ observation"}
		}
		raw, err := json.Marshal(map[string]any{"resolution": "matched", "hits": []any{hit}})
		testutil.FailErr(t, "encode recall search", err)
		original := "[recall#1]\n" + string(raw)
		msg := api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: original, EvidenceHandles: []string{"recall#1"}, ToolResult: &api.ToolResult{Tool: "recall", Content: original, Outcome: api.ToolResultOutcomeCompleted}}
		projected := transcript.Project([]api.Message{msg})
		got := projected[len(projected)-1].Content
		if strings.Contains(got, "search-row-id") || strings.Contains(got, `"hit_id"`) {
			t.Fatal("non-actionable search identity was presented as a recovery address")
		}
		for _, want := range []string{`"message_id":"original-message"`, `"handle":"read#1"`, `"currency":"changed"`, `"untrusted":true`, `"observed_at":"2026-10-14T00:00:00Z"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("projection lost %s", want)
			}
		}
		if withBody && (!strings.Contains(got, "recorded λ observation") || !strings.Contains(got, `"body_part":1`)) {
			t.Fatal("recorded body lost its mapping")
		}
		if msg.Content != original || msg.ToolResult.Content != original || !strings.Contains(original, "search-row-id") {
			t.Fatal("projection changed canonical result identity")
		}
		if again := transcript.Project(projected); again[len(again)-1].Content != got {
			t.Fatal("recall projection is not idempotent")
		}
	}
}

func TestSourceHistoryVersionProjectsExactSourceWithVersionLabel(t *testing.T) {
	version := `{"mode":"version","path":"app.go","recorded":true,"version_id":"ver-7","state":"present",` +
		`"content":"     3: \tif ok {\n     4: \t\treturn \"done\"","total_lines":9,"offset":3,"limit":2,"end_line":4,` +
		`"receipt":{"tool":"source_history","path":"app.go"}}`
	message := sourceReadMessage("[source_history#2]\n" + version)
	message.ToolResult.Tool = "source_history"
	message.EvidenceHandles = []string{"source_history#2"}
	projected := transcript.Project([]api.Message{message})
	got := projected[len(projected)-1].Content
	for _, want := range []string{
		`File contents "app.go" at version "ver-7" (lines 3–4); evidence "[source_history#2]":`,
		"\tif ok {\n\t\treturn \"done\"",
		`"version_id":"ver-7"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("source history projection lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "     3: ") || strings.Contains(got, `"content":`) {
		t.Fatalf("version source kept line labels or stayed in metadata: %s", got)
	}
	if again := transcript.Project(projected); again[len(again)-1].Content != got {
		t.Fatal("source history projection is not idempotent")
	}

	for name, body := range map[string]string{
		"effects":        `{"mode":"effects","path":"app.go","recorded":true,"receipt":{"tool":"source_history"}}`,
		"absent_version": `{"mode":"version","path":"app.go","state":"absent","receipt":{"tool":"source_history"}}`,
		"read_receipt":   strings.ReplaceAll(version, `"tool":"source_history"`, `"tool":"read"`),
	} {
		message := sourceReadMessage(body)
		message.ToolResult.Tool = "source_history"
		projected := transcript.Project([]api.Message{message})
		if got := projected[len(projected)-1].Content; strings.Contains(got, "Data from tool (source):") {
			t.Fatalf("%s projected as source: %s", name, got)
		}
	}
}
