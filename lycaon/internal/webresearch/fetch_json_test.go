package webresearch

import (
	"strings"
	"testing"
)

func TestExpandCompactJSONMakesOneLineBodiesPageable(t *testing.T) {
	compact := `{"sha":"abc","tree":[{"path":"a","type":"blob"},{"path":"b","type":"tree"}]}`
	got, expanded := expandCompactJSON("application/json; charset=utf-8", compact)
	if !expanded {
		t.Fatal("compact JSON was not expanded")
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) < 8 || !strings.Contains(got, `"path": "b"`) {
		t.Fatalf("expanded body = %q", got)
	}
	if strings.Count(got, `"path"`) != 2 {
		t.Fatalf("expansion changed the document: %q", got)
	}
}

func TestExpandCompactJSONLeavesOtherBodiesAlone(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		text        string
	}{
		{"plain text", "text/plain", `{"a":1}`},
		{"already indented", "application/json", "{\n  \"a\": 1\n}\n"},
		{"not a document", "application/json", `"just a string"`},
		{"malformed", "application/json", `{"a":`},
		{"empty", "application/json", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, expanded := expandCompactJSON(tc.contentType, tc.text)
			if expanded || got != tc.text {
				t.Fatalf("body changed: expanded=%v got=%q", expanded, got)
			}
		})
	}
	if got, ok := expandCompactJSON("application/vnd.api+json", `[{"id":1}]`); !ok || !strings.Contains(got, "\n") {
		t.Fatalf("structured +json suffix not expanded: %q", got)
	}
}

func TestFetchCacheExtPrefersDeclaredJSON(t *testing.T) {
	res := FetchURLResult{URL: "https://api.example.test/repos/x/git/trees/main?recursive=1", ContentType: "application/json; charset=utf-8"}
	if got := fetchCacheExt(res); got != "json" {
		t.Fatalf("ext = %q want json", got)
	}
	res = FetchURLResult{URL: "https://example.test/notes.txt", ContentType: "text/plain"}
	if got := fetchCacheExt(res); got != "txt" {
		t.Fatalf("ext = %q want txt", got)
	}
}
