package docrefs

import (
	"fmt"
	"strings"
	"testing"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "markdown link target",
			in:   "See [the server](lycaon/internal/api/server.go) for routes.",
			want: []string{"lycaon/internal/api/server.go"},
		},
		{
			name: "link anchor stripped",
			in:   "[naming](docs/naming.md#product-naming-canonical)",
			want: []string{"docs/naming.md"},
		},
		{
			name: "code span path",
			in:   "reconciled with `lycaon/internal/api/server.go` and more",
			want: []string{"lycaon/internal/api/server.go"},
		},
		{
			name: "backslash path normalized to slash",
			in:   "on Windows docs use `lycaon\\internal\\api\\server.go`",
			want: []string{"lycaon/internal/api/server.go"},
		},
		{
			name: "backslash link target normalized",
			in:   "see [routes](lycaon\\internal\\api\\server.go)",
			want: []string{"lycaon/internal/api/server.go"},
		},
		{
			name: "line suffix stripped",
			in:   "at `server.go:170` the router is built",
			want: []string{"server.go"},
		},
		{
			name: "line range suffix stripped",
			in:   "`app-state.ts:170-303` covers the store",
			want: []string{"app-state.ts"},
		},
		{
			name: "urls dropped",
			in:   "docs at [site](https://example.com/x) and <https://y.z/a>",
			want: nil,
		},
		{
			name: "bare identifiers and prose dropped",
			in:   "the `map[string]any` and `context.Context` types, plus `WorkerQueue`",
			want: []string{"context.Context"}, // has a dot; filesystem check drops it downstream
		},
		{
			name: "dedup by first appearance",
			in:   "`a/b.go` then [again](a/b.go) and `a/b.go`",
			want: []string{"a/b.go"},
		},
		{
			name: "links before code spans, in order",
			in:   "[one](x/one.go) text `y/two.go` and [three](z/three.go)",
			want: []string{"x/one.go", "z/three.go", "y/two.go"},
		},
		{
			name: "empty",
			in:   "no references here at all",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.in)
			if !equal(got, tc.want) {
				t.Fatalf("Extract() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtractCap confirms the candidate cap bounds output for pathological input.
func TestExtractCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "`dir%d/file.go` ", i)
	}
	if got := len(Extract(b.String())); got != maxCandidates {
		t.Fatalf("len(Extract) = %d, want cap %d", got, maxCandidates)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
