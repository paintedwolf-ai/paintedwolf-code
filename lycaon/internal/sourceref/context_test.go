package sourceref

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func location(path string) api.NavigationTarget {
	return api.NavigationTarget{ProjectID: "p", RootID: "r", Path: path, EntryKind: api.NavigationEntryKindFile}
}

func TestSuppliedUsesRetainedContentAndOriginalIdentity(t *testing.T) {
	source := location(".ignored/My File.md")
	source.WorkerID = "worker"
	context := &api.SourceContext{Locations: []api.NavigationTarget{source, location("dropped.go")}}
	got := ForResponse([]api.Message{{Content: `{"path":".ignored/My File.md"}`, SourceContext: context}, {Content: "invented.go"}}, "See `.ignored/My File.md` and invented.go")
	if len(got.Locations) != 1 || got.Locations[0] != source {
		t.Fatalf("supplied=%+v", got)
	}
	if got := ForResponse([]api.Message{{Content: "tool output removed", SourceContext: context}}, "See `.ignored/My File.md`"); len(got.Locations) != 0 {
		t.Fatalf("dropped output retained identity: %+v", got)
	}
	if len(context.Locations) != 2 {
		t.Fatal("projection mutated producer context")
	}
}

func TestMergePreservesAmbiguityAndBounds(t *testing.T) {
	context := &api.SourceContext{}
	for i := 0; i < api.MaxSourceContextLocations+1; i++ {
		context.Locations = append(context.Locations, location(fmt.Sprintf("%d/a.go", i)))
	}
	got := Merge(context, context)
	if len(got.Locations) != api.MaxSourceContextLocations || !got.Truncated {
		t.Fatalf("bounded context=%d truncated=%v", len(got.Locations), got.Truncated)
	}
	if !Mentioned(got, "a.go").Truncated {
		t.Fatal("projection lost incomplete context")
	}
}

func TestResponseSelectionPrecedesAggregateBound(t *testing.T) {
	var messages []api.Message
	for page := 0; page < 3; page++ {
		context := &api.SourceContext{}
		var content strings.Builder
		for i := 0; i < 2048; i++ {
			name := fmt.Sprintf("module%d/file%d.go", page, i)
			context.Locations = append(context.Locations, location(name))
			fmt.Fprintln(&content, name)
		}
		messages = append(messages, api.Message{Content: content.String(), SourceContext: context})
	}
	got := ForResponse(messages, "Updated module2/file2047.go")
	if got.Truncated || len(got.Locations) != 1 || got.Locations[0].Path != "module2/file2047.go" {
		t.Fatalf("unrelated context consumed response bound: %+v", got)
	}
}

func TestQualifiedRetentionDoesNotImportSameBasename(t *testing.T) {
	context := &api.SourceContext{Locations: []api.NavigationTarget{location("one/a.go"), location("two/a.go")}}
	got := ForResponse([]api.Message{{Content: "one/a.go", SourceContext: context}}, "a.go")
	if len(got.Locations) != 1 || got.Locations[0].Path != "one/a.go" {
		t.Fatalf("discarded identity returned: %+v", got)
	}
}

func TestRetainedPathsKeepTheirAddressScope(t *testing.T) {
	context := &api.SourceContext{Locations: []api.NavigationTarget{location("a.go"), location("nested/a.go")}}
	for _, tc := range []struct{ content, want string }{
		{"nested/a.go", "nested/a.go"},
		{"./nested/a.go", "nested/a.go"},
		{`nested\a.go`, "nested/a.go"},
		{"./a.go", "a.go"},
		{`.\a.go`, "a.go"},
		{"@root/nested/a.go", "nested/a.go"},
		{"@root/a.go", "a.go"},
	} {
		t.Run(tc.content, func(t *testing.T) {
			got := ForResponse([]api.Message{{Content: tc.content, SourceContext: context}}, "a.go")
			if len(got.Locations) != 1 || got.Locations[0].Path != tc.want {
				t.Fatalf("retained path changed scope: %+v", got)
			}
		})
	}
}

func TestSourceContextRetainsDirectoryAbbreviations(t *testing.T) {
	target := location("packages/component/scripts")
	target.EntryKind = api.NavigationEntryKindFolder
	context := &api.SourceContext{Locations: []api.NavigationTarget{target}}
	got := ForResponse([]api.Message{{Content: "`packages/component/scripts/`", SourceContext: context}}, "See scripts/.")
	if len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("directory identity was lost: %+v", got)
	}
}

func TestSourceContextRetainsSpacedFileLineAddresses(t *testing.T) {
	target := location("notes/My File.md")
	context := &api.SourceContext{Locations: []api.NavigationTarget{target}}
	for _, response := range []string{"`My File.md:12`", "`My File.md#L12-L20`", "`notes/My File.md:12-20`"} {
		got := ForResponse([]api.Message{{Content: `{"path":"notes/My File.md"}`, SourceContext: context}}, response)
		if len(got.Locations) != 1 || got.Locations[0] != target {
			t.Fatalf("line address %q lost its source: %+v", response, got)
		}
	}
}

func TestSourceContextExtensionlessRootRequiresSyntaxOrQuote(t *testing.T) {
	target := location("task")
	context := &api.SourceContext{Locations: []api.NavigationTarget{target}}

	// Bare prose words do not retain extensionless root locations.
	if got := Mentioned(context, "Our primary task is to run the verification suite."); len(got.Locations) != 0 {
		t.Fatalf("bare prose word retained extensionless root file: %+v", got)
	}

	// Backticks retain the location.
	if got := Mentioned(context, "Run `task` to inspect targets."); len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("backticked file was not retained: %+v", got)
	}

	// Path prefixes retain the location.
	if got := Mentioned(context, "Run ./task check-fast."); len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("path-prefixed file was not retained: %+v", got)
	}
}
