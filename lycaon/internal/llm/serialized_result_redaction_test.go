package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A file tool serializes what it read as a JSON string before any screen sees
// it, so a managed value with a quote or backslash reaches storage in its
// escaped spelling. That spelling is still the value.
func TestStorageReferencesAManagedValueInsideASerializedToolResult(t *testing.T) {
	for name, secret := range map[string]string{
		"backslash":        `Az7\Kp9!Tr2zz`,
		"double quote":     `pa"ss-word-9911`,
		"gcp key fragment": `"private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg"`,
	} {
		t.Run(name, func(t *testing.T) {
			matcher := modelScreenMatcher(t)
			matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
				evidence := liveManagedEvidence()
				evidence.Secret = secret
				return []secretmatch.HarvestedValue{evidence}
			})
			var serialized bytes.Buffer
			encoder := json.NewEncoder(&serialized)
			encoder.SetEscapeHTML(false)
			testutil.FailErr(t, "encode read result", encoder.Encode(map[string]any{
				"path": "config/settings.py", "mode": "content", "content": "1\tDB_PASSWORD=" + secret + "\n2\tPORT=3000",
			}))
			msg := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read", Content: serialized.String()}}

			stored, changed := RedactMessageForStorage(context.Background(), matcher, msg)

			quoted, _ := json.Marshal(secret)
			if !changed || strings.Contains(stored.ToolResult.Content, strings.Trim(string(quoted), `"`)) {
				t.Fatalf("the escaped spelling reached storage: %s", stored.ToolResult.Content)
			}
			var result map[string]any
			testutil.FailErr(t, "decode stored result", json.Unmarshal([]byte(stored.ToolResult.Content), &result))
			if result["content"] != "1\tDB_PASSWORD="+valueReference+"\n2\tPORT=3000" {
				t.Fatalf("content = %q, want the reference in place of the value", result["content"])
			}
			if stored.HostSecretRedaction.References() != 1 {
				t.Fatalf("references = %d, want the one replacement recorded as a reference", stored.HostSecretRedaction.References())
			}
		})
	}
}
