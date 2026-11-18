package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// TestSurveyJSONMarshalKeepsHTMLLiteral covers exact source text in model context.
func TestSurveyJSONMarshalKeepsHTMLLiteral(t *testing.T) {
	t.Parallel()
	type response struct {
		Content string `json:"content"`
	}
	raw, err := surveyjson.Marshal(response{Content: "<div> & </div>"})
	if err != nil {
		t.Fatalf("surveyjson.Marshal: %v", err)
	}
	out := string(raw)
	for _, want := range []string{"<div>", "</div>", "& "} {
		if !strings.Contains(out, want) {
			t.Fatalf("survey marshal lost literal %q in %q", want, out)
		}
	}
}

// TestSurveyJSONMarshalNoTrailingNewline protects concatenated envelopes.
func TestSurveyJSONMarshalNoTrailingNewline(t *testing.T) {
	t.Parallel()
	raw, err := surveyjson.Marshal(map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("surveyjson.Marshal: %v", err)
	}
	if len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == ' ') {
		t.Fatalf("surveyjson.Marshal output must be trimmed of trailing whitespace, got %q", string(raw))
	}
}
