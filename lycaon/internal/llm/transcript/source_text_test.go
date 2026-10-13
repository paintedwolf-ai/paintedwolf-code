package transcript_test

import (
 "encoding/json"
 "strings"
 "testing"

 "github.com/lycaon/lycaon/internal/llm/transcript"
 "github.com/lycaon/lycaon/pkg/api"
)

func TestReadProjectionSeparatesExactSourceAndLocations(t *testing.T) {
 source := "    return \"quoted\"\n\n\tfinish()"
 body, err := json.Marshal(map[string]any{
  "path": "source.py", "mode": "content", "offset": 7, "end_line": 9,
  "content": "     7:     return \"quoted\"\n     8: \n     9: \tfinish()",
  "receipt": map[string]any{"tool": "read", "path": "source.py", "truncated": false},
 })
 if err != nil { t.Fatalf("encode read result: %v", err) }
 original := "[read#1]\n" + string(body)
 message := api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
  Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted,
  Content: original, EvidenceHandles: []string{"read#1"},
  ToolResult: &api.ToolResult{Tool: "read", Content: original, Outcome: api.ToolResultOutcomeCompleted},
 }
 out := transcript.Project([]api.Message{message})
 got := out[len(out)-1].Content
 marked := "⟦D⟧" + strings.ReplaceAll(source, "\n", "\n⟦D⟧")
 if !strings.Contains(got, marked) { t.Fatalf("exact source absent from projection: %s", got) }
 for _, want := range []string{"[read#1]", `"offset":7`, `"end_line":9`, `"receipt":`} {
  if !strings.Contains(got, want) { t.Fatalf("location or receipt %q absent: %s", want, got) }
 }
 if message.Content != original || message.ToolResult.Content != original { t.Fatal("projection changed retained result") }
 if strings.Contains(got, "     7: ") || strings.Contains(got, `\n`) { t.Fatalf("source still contains display labels or JSON escapes: %s", got) }
}
