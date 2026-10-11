package projectsource

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseSourceQueryReadsPastedLocations(t *testing.T) {
	posix := SourcePathStyle{Home: "/Users/Me"}
	windows := SourcePathStyle{Windows: true, Home: `C:\Users\Me`}
	abs := func(path string) SourceQuery { return SourceQuery{Path: strings.ToLower(path), Absolute: path} }
	at := func(q SourceQuery, line, column, end int) SourceQuery {
		q.Line, q.Column, q.EndLine = line, column, end
		return q
	}
	rel := func(path string) SourceQuery { return SourceQuery{Path: path} }
	for _, tc := range []struct {
		name  string
		style SourcePathStyle
		raw   string
		want  SourceQuery
	}{
		{"fuzzy text", posix, "  Release ", rel("release")},
		{"relative path", posix, "docs/operations/release.md", rel("docs/operations/release.md")},
		{"dot relative", posix, "./././docs/a.md", rel("docs/a.md")},
		{"absolute", posix, "/Users/Me/src/App/main.go", abs("/Users/Me/src/App/main.go")},
		{"home", posix, "~/src/app/main.go", abs("/Users/Me/src/app/main.go")},
		{"bare home", posix, "~", abs("/Users/Me")},
		{"tilde inside a name", posix, "notes~/a", rel("notes~/a")},
		{"file url", posix, "file:///Users/Me/My%20App/a.go", abs("/Users/Me/My App/a.go")},
		{"file url on a share", posix, "file://server/share/a.go", abs("//server/share/a.go")},
		{"quoted", posix, `"/tmp/My App/a.go"`, abs("/tmp/My App/a.go")},
		{"backticked with location", posix, "`src/a.go:12:3`", at(rel("src/a.go"), 12, 3, 0)},
		{"shell escapes", posix, `/tmp/My\ App/\(draft\).md`, abs("/tmp/My App/(draft).md")},
		{"line", posix, "src/a.go:12", at(rel("src/a.go"), 12, 0, 0)},
		{"line range", posix, "src/a.go:10-20", at(rel("src/a.go"), 10, 0, 20)},
		{"reversed range keeps the start", posix, "src/a.go:20-10", at(rel("src/a.go"), 20, 0, 0)},
		{"compiler trailing colon", posix, "src/a.go:12:3:", at(rel("src/a.go"), 12, 3, 0)},
		{"zero column keeps the line", posix, "src/a.go:12:0", at(rel("src/a.go"), 12, 0, 0)},
		{"zero line is text", posix, "src/a.go:0", rel("src/a.go:0")},
		{"code host anchor", posix, "src/a.go#L40", at(rel("src/a.go"), 40, 0, 0)},
		{"code host range", posix, "src/a.go#L40C2-L52", at(rel("src/a.go"), 40, 2, 52)},
		{"gitlab range", posix, "src/a.go#L40-52", at(rel("src/a.go"), 40, 0, 52)},
		{"bitbucket range", posix, "src/a.go#lines-40:52", at(rel("src/a.go"), 40, 0, 52)},
		{"msbuild", posix, "src/a.cs(12,3)", at(rel("src/a.cs"), 12, 3, 0)},
		{"numbered copy is a name", posix, "report (1)", rel("report (1)")},
		{"numbered copy with extension", posix, "report (1).pdf", rel("report (1).pdf")},
		{"location alone is text", posix, ":12", rel(":12")},
		{"typing the colon", posix, "a.ts:", rel("a.ts")},
		{"colon inside a name", posix, "foo:bar.ts:9", at(rel("foo:bar.ts"), 9, 0, 0)},
		{"file url with location", posix, "file:///repo/a.go#L7", at(abs("/repo/a.go"), 7, 0, 0)},
		{"javascript stack frame", posix, "    at render (src/chat/View.tsx:212:9)", at(rel("src/chat/view.tsx"), 212, 9, 0)},
		{"go panic frame", posix, "\t/Users/Me/src/app/build.go:311 +0x1d4", at(abs("/Users/Me/src/app/build.go"), 311, 0, 0)},
		{"go test failure", posix, "FAIL lycaon/internal/app build_test.go:77", at(rel("lycaon/internal/app/build_test.go"), 77, 0, 0)},
		{"go test message", posix, "    build_test.go:77: got 3, want 4", at(rel("build_test.go"), 77, 0, 0)},
		{"rust panic", posix, "thread 'main' panicked at src/main.rs:5:9:", at(rel("src/main.rs"), 5, 9, 0)},
		{"typescript error", posix, "src/a.ts(12,3): error TS2322: Type 'x'", at(rel("src/a.ts"), 12, 3, 0)},
		{"python traceback", posix, `  File "/srv/app/main.py", line 42, in handler`, at(abs("/srv/app/main.py"), 42, 0, 0)},
		{"path with spaces and a location", posix, "My Folder/a.go:12", at(rel("folder/a.go"), 12, 0, 0)},
		{"dev server url", posix, "http://localhost:5173/src/chat/View.tsx?t=1712:212:9", at(abs("/src/chat/View.tsx"), 212, 9, 0)},
		{"dev server file outside its root", posix, "http://localhost:5173/@fs/Users/Me/lib/a.ts", abs("/Users/Me/lib/a.ts")},
		{"bundler scheme", posix, "webpack-internal:///./src/a.js:3:1", at(rel("src/a.js"), 3, 1, 0)},
		{"windows drive", windows, `C:\Users\Me\Src\main.go:9`, at(abs("C:/Users/Me/Src/main.go"), 9, 0, 0)},
		{"windows mixed separators", windows, `src\app/main.go`, rel("src/app/main.go")},
		{"windows dot relative", windows, `.\src\main.go`, rel("src/main.go")},
		{"windows home", windows, `~\src\main.go`, abs("C:/Users/Me/src/main.go")},
		{"windows unc", windows, `\\server\share\main.go`, abs("//server/share/main.go")},
		{"windows extended length", windows, `\\?\C:\src\main.go`, abs("C:/src/main.go")},
		{"windows extended unc", windows, `\\?\UNC\server\share\main.go`, abs("//server/share/main.go")},
		{"windows file url", windows, "file:///C:/Src/main.go", abs("C:/Src/main.go")},
		{"windows msbuild", windows, `C:\src\a.cs(12,3)`, at(abs("C:/src/a.cs"), 12, 3, 0)},
		{"windows compiler line", windows, `C:\src\a.cs(12,3): error CS1002: ; expected`, at(abs("C:/src/a.cs"), 12, 3, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseSourceQuery(tc.raw, tc.style); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseSourceQuery(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseSourceQueryReadsCodeHostLinks(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want SourceQuery
	}{
		{"https://github.com/Painted/Wolf/blob/main/docs/a.md#L10-L20",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "github.com/painted/wolf", Segments: []string{"main", "docs", "a.md"}}, Line: 10, EndLine: 20}},
		{"https://github.com/painted/wolf/blob/feature/x/src/a.go#L3",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "github.com/painted/wolf", Segments: []string{"feature", "x", "src", "a.go"}}, Line: 3}},
		{"https://raw.githubusercontent.com/painted/wolf/main/src/a.go",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "github.com/painted/wolf", Segments: []string{"main", "src", "a.go"}}}},
		{"https://gitlab.example.com/group/sub/wolf/-/blob/main/src/a.go#L4-9",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "gitlab.example.com/group/sub/wolf", Segments: []string{"main", "src", "a.go"}}, Line: 4, EndLine: 9}},
		{"https://codeberg.org/painted/wolf/src/branch/main/src/a.go#L2",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "codeberg.org/painted/wolf", Segments: []string{"main", "src", "a.go"}}, Line: 2}},
		{"https://bitbucket.org/painted/wolf/src/abc123/src/a.go#lines-5:8",
			SourceQuery{Remote: &SourceRemoteFile{Repo: "bitbucket.org/painted/wolf", Segments: []string{"abc123", "src", "a.go"}}, Line: 5, EndLine: 8}},
	} {
		if got := ParseSourceQuery(tc.raw, SourcePathStyle{}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseSourceQuery(%q) = %+v %+v, want %+v %+v", tc.raw, got, got.Remote, tc.want, tc.want.Remote)
		}
	}
}

func TestSourceRemoteRepoNormalizesRemoteSpellings(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:Painted/Wolf.git":           "github.com/painted/wolf",
		"https://github.com/painted/wolf":           "github.com/painted/wolf",
		"https://user@github.com/painted/wolf.git/": "github.com/painted/wolf",
		"ssh://git@gitlab.example.com:22/g/s/w.git": "gitlab.example.com/g/s/w",
		"/srv/git/wolf.git":                         "",
		"file:///srv/git/wolf.git":                  "",
		"git@github.com:":                           "",
	} {
		if got := SourceRemoteRepo(remote); got != want {
			t.Errorf("SourceRemoteRepo(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestSourcePathReadingsSplitAtRootBoundaries(t *testing.T) {
	windows := SourcePathStyle{Windows: true}
	roots := map[string][]string{
		"app":  {"c:/users/me/src/app"},
		"docs": {"c:/users/me/docs", "d:/mirror/docs"},
	}
	for _, tc := range []struct {
		raw  string
		want []sourcePathReading
	}{
		{"main", []sourcePathReading{{rest: "main"}}},
		{`C:\Users\Me\Src\App\cmd\main.go`, []sourcePathReading{
			{rest: "c:/users/me/src/app/cmd/main.go"},
			{rootID: "app", rest: "cmd/main.go", anchor: 20},
		}},
		{"app/cmd", []sourcePathReading{{rest: "app/cmd"}, {rootID: "app", rest: "cmd", anchor: 4}}},
		{"pp/cmd", []sourcePathReading{{rest: "pp/cmd"}, {rootID: "app", rest: "cmd", anchor: 3}}},
		{"src/app/", []sourcePathReading{{rest: "src/app/"}, {rootID: "app", rest: "", anchor: 8}}},
		{"/guide.md", []sourcePathReading{
			{rest: "/guide.md"},
			{rootID: "app", rest: "guide.md", anchor: 1},
			{rootID: "docs", rest: "guide.md", anchor: 1},
		}},
		{`D:\Mirror\Docs\guide.md`, []sourcePathReading{
			{rest: "d:/mirror/docs/guide.md"},
			{rootID: "docs", rest: "guide.md", anchor: 15},
		}},
	} {
		query := ParseSourceQuery(tc.raw, windows).Path
		if got := sourcePathReadings(query, roots); !slices.Equal(got, tc.want) {
			t.Errorf("readings(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}
