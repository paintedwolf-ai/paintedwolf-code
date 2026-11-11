package evidence

import (
	"slices"
	"testing"
)

func TestPopulateReadRecordCoversEachReturnedLineSpan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content string
		want          []LineRange
		body          []string
	}{
		{
			name:    "explicit range",
			content: `{"path":"a.go","ranges":[{"offset":3,"end_line":5,"content":"three\n"}]}`,
			want:    []LineRange{{Start: 3, End: 5}},
			body:    []string{"three"},
		},
		{
			name:    "range without bounds counts its text",
			content: `{"path":"a.go","ranges":[{"offset":0,"end_line":0,"content":"one\ntwo\n"}]}`,
			want:    []LineRange{{Start: 1, End: 2}},
			body:    []string{"one\ntwo"},
		},
		{
			name:    "empty range keeps one line",
			content: `{"path":"a.go","ranges":[{"offset":7,"end_line":0,"content":""}]}`,
			want:    []LineRange{{Start: 7, End: 7}},
		},
		{
			name:    "symbol span",
			content: `{"path":"a.go","mode":"symbol","start_line":4,"end_line":6,"content":"body"}`,
			want:    []LineRange{{Start: 4, End: 6}},
			body:    []string{"body"},
		},
		{
			name:    "file text without bounds",
			content: `{"path":"a.go","content":"one\ntwo\n"}`,
			want:    []LineRange{{Start: 1, End: 2}},
			body:    []string{"one\ntwo"},
		},
		{name: "outline has no file text", content: `{"path":"a.go","mode":"outline","content":"func a"}`},
		{name: "empty symbol has no file text", content: `{"path":"a.go","mode":"symbol","content":"  "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &Record{}
			populateReadRecord("", rec, map[string]any{"path": "a.go"}, tc.content)
			if !slices.Equal(rec.LineRanges, tc.want) {
				t.Fatalf("line ranges = %+v, want %+v", rec.LineRanges, tc.want)
			}
			if !slices.Equal(rec.Body, tc.body) {
				t.Fatalf("body = %q, want %q", rec.Body, tc.body)
			}
		})
	}
}
