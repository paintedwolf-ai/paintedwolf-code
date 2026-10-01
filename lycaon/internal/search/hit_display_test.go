package search

import "testing"

func TestProjectHitDisplayCode(t *testing.T) {
	hit := Hit{HitKind: "code", Path: "a.go", Line: 12, Snippet: "func main()"}
	ProjectHitDisplay(&hit)
	if hit.Title != "func main()" {
		t.Fatalf("title = %q", hit.Title)
	}
	if hit.Context != "a.go:12" {
		t.Fatalf("context = %q", hit.Context)
	}
}

func TestProjectHitDisplayRequiresExplicitFilePath(t *testing.T) {
	hit := Hit{HitKind: "file", SourceRef: "src/main.go"}
	ProjectHitDisplay(&hit)
	if hit.Title != "File" || hit.Context != "" {
		t.Fatalf("display = (%q, %q)", hit.Title, hit.Context)
	}
}

func TestProjectHitDisplayToolPrefersToolNameOverJSONBody(t *testing.T) {
	hit := Hit{
		HitKind:   "tool",
		Tool:      "write",
		SourceRef: "call_e83a96ca",
		Snippet:   `{"content":"[package]\nname = \"mylib\""}`,
	}
	ProjectHitDisplay(&hit)
	if hit.Title != "write" {
		t.Fatalf("title = %q, want the tool name", hit.Title)
	}
	if hit.Context != "call_e83a96ca" {
		t.Fatalf("context = %q, want the call ref rather than a JSON body", hit.Context)
	}
}

func TestProjectHitDisplayToolKeepsProseSummary(t *testing.T) {
	hit := Hit{
		HitKind:   "tool",
		Tool:      "command",
		SourceRef: "call_a1",
		Snippet:   "docker --version\nDocker version 27.1.1",
	}
	ProjectHitDisplay(&hit)
	if hit.Title != "command" {
		t.Fatalf("title = %q", hit.Title)
	}
	if hit.Context != "docker --version" {
		t.Fatalf("context = %q, want the readable first line", hit.Context)
	}
}

func TestProjectHitDisplayToolWithoutNameNeverTitlesJSON(t *testing.T) {
	hit := Hit{HitKind: "tool", SourceRef: "call_b2", Snippet: `{"status":"updated"}`}
	ProjectHitDisplay(&hit)
	if hit.Title != "Tool result" {
		t.Fatalf("title = %q, want a label rather than the JSON body", hit.Title)
	}
}

func TestProjectHitDisplayDoesNotOverwrite(t *testing.T) {
	hit := Hit{HitKind: "code", Title: "host", Path: "a.go", Snippet: "x"}
	ProjectHitDisplay(&hit)
	if hit.Title != "host" {
		t.Fatalf("title overwritten: %q", hit.Title)
	}
}

func TestProjectHitDisplayToolCallTrimsNamePrefixFromSnippet(t *testing.T) {
	hit := Hit{
		HitKind:   "tool",
		Tool:      "write",
		Path:      "mylib/src/lib.rs",
		SourceRef: "call_c1",
		Snippet:   `write {"path":"mylib/src/lib.rs","content":"pub fn greet()"}`,
	}
	ProjectHitDisplay(&hit)
	if hit.Title != "write" {
		t.Fatalf("title = %q", hit.Title)
	}
	if hit.Context != "mylib/src/lib.rs" {
		t.Fatalf("context = %q, want the path rather than encoded args", hit.Context)
	}
}

func TestProjectHitDisplayWebUsesStructuredTitleAndURL(t *testing.T) {
	hit := Hit{
		HitKind: "web",
		URL:     "https://example.test/docs",
		Snippet: `{"title":"Reference","url":"https://example.test/docs"}`,
	}
	ProjectHitDisplay(&hit)
	if hit.Title != "Reference" || hit.Context != hit.URL {
		t.Fatalf("display = (%q, %q)", hit.Title, hit.Context)
	}
}
