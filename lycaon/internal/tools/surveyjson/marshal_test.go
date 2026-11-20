package surveyjson

import "testing"

// Model context reads source text exactly: HTML characters stay literal and
// no trailing newline separates concatenated envelopes.
func TestMarshalKeepsHTMLLiteralAndTrimmed(t *testing.T) {
	type response struct {
		Content string `json:"content"`
	}
	in := response{Content: "<div> & </div>"}
	for name, tc := range map[string]struct {
		marshal func() ([]byte, error)
		want    string
	}{
		"marshal":        {func() ([]byte, error) { return Marshal(in) }, `{"content":"<div> & </div>"}`},
		"marshal indent": {func() ([]byte, error) { return MarshalIndent(in, "", "  ") }, "{\n  \"content\": \"<div> & </div>\"\n}"},
	} {
		raw, err := tc.marshal()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(raw) != tc.want {
			t.Fatalf("%s = %q, want %q", name, raw, tc.want)
		}
	}
}
