package hostmarker

import (
	"strings"
	"testing"
)

func TestSplitToolJSONBodyPlain(t *testing.T) {
	in := `{"results":[1],"total_results":1}`
	prefix, body, suffix, ok := SplitToolJSONBody(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != "" || suffix != "" {
		t.Fatalf("prefix=%q suffix=%q", prefix, suffix)
	}
	if body != in {
		t.Fatalf("body=%q", body)
	}
}

func TestSplitToolJSONBodyHandlePrefixed(t *testing.T) {
	in := "[find#1]\n{\"results\":[],\"total_results\":0}"
	prefix, body, suffix, ok := SplitToolJSONBody(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != "[find#1]\n" {
		t.Fatalf("prefix=%q", prefix)
	}
	if !strings.HasPrefix(body, "{") || suffix != "" {
		t.Fatalf("body=%q suffix=%q", body, suffix)
	}
}

func TestSplitToolJSONBodyBannerSuffix(t *testing.T) {
	in := "[grep#2]\n{\"matches\":[]}\n>>> Tool feedback\nCode: X"
	prefix, body, suffix, ok := SplitToolJSONBody(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != "[grep#2]\n" {
		t.Fatalf("prefix=%q", prefix)
	}
	if body != `{"matches":[]}` {
		t.Fatalf("body=%q", body)
	}
	if !strings.HasPrefix(suffix, "\n>>> Tool feedback") {
		t.Fatalf("suffix=%q", suffix)
	}
}

func TestSplitToolJSONBodyNonJSON(t *testing.T) {
	_, _, _, ok := SplitToolJSONBody("plain stdout\nline 2")
	if ok {
		t.Fatal("expected !ok")
	}
}

func TestSplitToolJSONBodyRequiresDeclaredPrefix(t *testing.T) {
	for _, input := range []string{
		`package main; func main() {}`,
		`const result = {"content":"source text"};`,
		`Report: {"next_offset":5}`,
		`[unknown] {"next_offset":5}`,
		`[read#0] {"next_offset":5}`,
	} {
		if _, _, _, ok := SplitToolJSONBody(input); ok {
			t.Errorf("ordinary output parsed as JSON: %q", input)
		}
	}
}

func TestSplitToolJSONBodyPreservesDeclaredEnvelope(t *testing.T) {
	for _, prefix := range []string{
		"[root:child:read#2]\n",
		"[host:overlay-promote-event] ",
		"[promote_overlay#1]\n[host:overlay-promote-event] ",
		"[host:overlay-reject-event]\n",
		"[compacted tool_result]\n",
		"[compacted tool_result]\n[read#1]\n",
		"[compacted tool_result]\nMap: retained excerpt\nverbatim head/tail:\n[root:child:read#1]\n",
	} {
		body := `{ "content": "literal } and \"{\"", "nested": {} }`
		suffix := "\n>>> Host feedback\nCode: EXAMPLE\nverbatim head/tail:\n{\"message\":\"feedback\"}"
		gotPrefix, gotBody, gotSuffix, ok := SplitToolJSONBody(prefix + body + suffix)
		if !ok || gotPrefix != prefix || gotBody != body || gotSuffix != suffix {
			t.Errorf("split %q: prefix=%q body=%q suffix=%q ok=%v", prefix, gotPrefix, gotBody, gotSuffix, ok)
		}
	}
}
