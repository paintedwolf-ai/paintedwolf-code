package argv

import (
	"errors"
	"reflect"
	"testing"
)

func TestSplitCommandLineBasics(t *testing.T) {
	cases := []struct {
		line string
		name string
		args []string
	}{
		{`go test ./...`, "go", []string{"test", "./..."}},
		{`echo "two words"`, "echo", []string{"two words"}},
		{`echo 'single quoted'`, "echo", []string{"single quoted"}},
		{`grep "a|b" file`, "grep", []string{"a|b", "file"}},
		{`grep 'a"b' file`, "grep", []string{`a"b`, "file"}},
		{`python3 -c "import x; x.run()"`, "python3", []string{"-c", "import x; x.run()"}},
		{"go\ttest\t./...", "go", []string{"test", "./..."}},
		{`ad"joi"ned`, "adjoined", []string{}},
	}
	for _, c := range cases {
		env, name, args, err := SplitCommandLine(c.line)
		if err != nil {
			t.Fatalf("SplitCommandLine(%q): %v", c.line, err)
		}
		if len(env) != 0 {
			t.Errorf("SplitCommandLine(%q) unexpected env: %v", c.line, env)
		}
		if args == nil {
			args = []string{}
		}
		if name != c.name || !reflect.DeepEqual(args, c.args) {
			t.Errorf("SplitCommandLine(%q) = %q %q, want %q %q", c.line, name, args, c.name, c.args)
		}
	}
}

func TestSplitCommandLineEnvironmentVariables(t *testing.T) {
	cases := []struct {
		line string
		env  map[string]string
		name string
		args []string
	}{
		{
			line: "PORT=8080 go run .",
			env:  map[string]string{"PORT": "8080"},
			name: "go",
			args: []string{"run", "."},
		},
		{
			line: `A=1 B="two words" C='single' D= ./app`,
			env:  map[string]string{"A": "1", "B": "two words", "C": "single", "D": ""},
			name: "./app",
			args: []string{},
		},
		{
			line: `"PORT=8080" go run .`,
			env:  nil,
			name: "PORT=8080",
			args: []string{"go", "run", "."},
		},
		{
			line: "FOO=bar=baz ./app arg",
			env:  map[string]string{"FOO": "bar=baz"},
			name: "./app",
			args: []string{"arg"},
		},
		{
			line: `./FOO=bar ./app`,
			env:  nil,
			name: "./FOO=bar",
			args: []string{"./app"},
		},
		{
			line: `cmd FOO=bar`,
			env:  nil,
			name: "cmd",
			args: []string{"FOO=bar"},
		},
	}
	for _, c := range cases {
		env, name, args, err := SplitCommandLine(c.line)
		if err != nil {
			t.Fatalf("SplitCommandLine(%q): %v", c.line, err)
		}
		if args == nil {
			args = []string{}
		}
		if !reflect.DeepEqual(env, c.env) {
			t.Errorf("SplitCommandLine(%q) env = %v, want %v", c.line, env, c.env)
		}
		if name != c.name || !reflect.DeepEqual(args, c.args) {
			t.Errorf("SplitCommandLine(%q) = %q %q, want %q %q", c.line, name, args, c.name, c.args)
		}
	}
}

func TestSplitCommandLineEmptyQuotedArgSurvives(t *testing.T) {
	_, name, args, err := SplitCommandLine(`printf %s ""`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []string{"%s", ""}
	if name != "printf" || !reflect.DeepEqual(args, want) {
		t.Errorf("got %q %q, want printf %q", name, args, want)
	}
}

func TestSplitCommandLineEscapesInsideDoubleQuotes(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`echo "a\"b"`, `a"b`},
		{`echo "a\\b"`, `a\b`},
		{`grep "a\|b"`, `a\|b`},
		{`echo "trail\\"`, `trail\`},
	}
	for _, c := range cases {
		_, _, args, err := SplitCommandLine(c.line)
		if err != nil {
			t.Fatalf("SplitCommandLine(%q): %v", c.line, err)
		}
		if len(args) != 1 || args[0] != c.want {
			t.Errorf("SplitCommandLine(%q) args = %q, want [%q]", c.line, args, c.want)
		}
	}
}

func TestSplitCommandLineRejections(t *testing.T) {
	cases := []struct {
		line string
		want error
	}{
		{`echo a | head`, ErrShellMetacharacters},
		{`git status && git diff`, ErrShellMetacharacters},
		{`echo $HOME`, ErrShellMetacharacters},
		{`echo a; echo b`, ErrShellMetacharacters},
		{`echo a >`, ErrRedirectionTargetRequired},
		{`echo a >>`, ErrRedirectionTargetRequired},
		{`echo a 2>`, ErrRedirectionTargetRequired},
		{`echo a <`, ErrRedirectionTargetRequired},
		{`echo a > out|txt`, ErrShellMetacharacters},
		{`grep "open`, ErrUnterminatedQuote},
		{`echo "trail\`, ErrUnterminatedQuote},
		{"git status\ngit diff", ErrUnquotedNewline},
		{"PORT=8080", ErrEnvAssignmentCommand},
		{"A=1 B=2", ErrEnvAssignmentCommand},
	}
	for _, c := range cases {
		if c.want == nil {
			continue
		}
		_, _, _, err := SplitCommandLine(c.line)
		if !errors.Is(err, c.want) {
			t.Errorf("SplitCommandLine(%q) err = %v, want %v", c.line, err, c.want)
		}
	}
}

func TestSplitElementRedirection(t *testing.T) {
	file := func(path string, appendMode bool) Sink { return Sink{Kind: SinkFile, Path: path, Append: appendMode} }
	cases := []struct {
		line   string
		args   []string
		stdout Sink
		stderr Sink
		stdin  Source
	}{
		{line: "echo a > out.txt", args: []string{"a"}, stdout: file("out.txt", false)},
		{line: "echo a >> out.txt", args: []string{"a"}, stdout: file("out.txt", true)},
		{line: "echo a >out.txt", args: []string{"a"}, stdout: file("out.txt", false)},
		{line: `echo a >"out file.txt"`, args: []string{"a"}, stdout: file("out file.txt", false)},
		{line: "pytest -v 2> err.log", args: []string{"-v"}, stderr: file("err.log", false)},
		{line: "pytest -v 2>> err.log", args: []string{"-v"}, stderr: file("err.log", true)},
		{line: "cmd > out 2>> err", stdout: file("out", false), stderr: file("err", true)},
		{line: "build &> build.log", stdout: file("build.log", false), stderr: file("build.log", false)},
		{line: "build >& build.log", stdout: file("build.log", false), stderr: file("build.log", false)},
		{line: "sort < in.txt", stdin: Source{Path: "in.txt"}},
		{line: "cmd < /dev/null", stdin: Source{Null: true}},
		{line: "cmd > out 2>&1", stdout: file("out", false), stderr: file("out", false)},
		{line: "cmd 2>&1 > out", stdout: file("out", false), stderr: Sink{Kind: SinkStdout}},
		{line: "cmd 2>&1 >/dev/null", stdout: Sink{Kind: SinkNull}, stderr: Sink{Kind: SinkStdout}},
		{line: "cmd >/dev/null 2>&1", stdout: Sink{Kind: SinkNull}, stderr: Sink{Kind: SinkNull}},
		{line: "cmd >&1", stdout: Sink{Kind: SinkStdout}},
		{line: "cmd 1>&2", stdout: Sink{Kind: SinkStderr}},
		{line: "cmd a2>b", args: []string{"a2"}, stdout: file("b", false)},
		{line: `cmd "2">b`, args: []string{"2"}, stdout: file("b", false)},
		{line: `cmd 2 >b`, args: []string{"2"}, stdout: file("b", false)},
		{line: `cmd a\>b`, args: []string{"a>b"}},
		{line: `cmd '>' b`, args: []string{">", "b"}},
	}
	for _, c := range cases {
		el, err := SplitElement(c.line)
		if err != nil {
			t.Fatalf("SplitElement(%q): %v", c.line, err)
		}
		args := el.Args
		if args == nil {
			args = []string{}
		}
		if c.args == nil {
			c.args = []string{}
		}
		if !reflect.DeepEqual(args, c.args) {
			t.Errorf("%q args = %q, want %q", c.line, args, c.args)
		}
		wantStdout, wantStderr := c.stdout, c.stderr
		if wantStdout.Kind == SinkDefault {
			wantStdout = Sink{Kind: SinkStdout}
		}
		if wantStderr.Kind == SinkDefault {
			wantStderr = Sink{Kind: SinkStderr}
		}
		if got := el.Streams.StdoutSink(); got != wantStdout {
			t.Errorf("%q stdout = %+v, want %+v", c.line, got, wantStdout)
		}
		if got := el.Streams.StderrSink(); got != wantStderr {
			t.Errorf("%q stderr = %+v, want %+v", c.line, got, wantStderr)
		}
		if el.Streams.Stdin != c.stdin {
			t.Errorf("%q stdin = %+v, want %+v", c.line, el.Streams.Stdin, c.stdin)
		}
	}
}

func TestSplitElementRejectsRedirectionsItCannotRun(t *testing.T) {
	cases := []struct {
		line  string
		want  error
		issue RedirectionIssue
	}{
		{line: "cmd 3> f", want: ErrRedirectionUnsupported, issue: IssueDescriptorUnsupported},
		{line: "cmd 3>f", want: ErrRedirectionUnsupported, issue: IssueDescriptorUnsupported},
		{line: "cmd 0> f", want: ErrRedirectionUnsupported, issue: IssueDescriptorUnsupported},
		{line: "cmd 2< f", want: ErrRedirectionUnsupported, issue: IssueDescriptorUnsupported},
		{line: "cmd 2>&3", want: ErrRedirectionUnsupported, issue: IssueDescriptorUnsupported},
		{line: "cmd >&-", want: ErrRedirectionUnsupported, issue: IssueOperatorUnsupported},
		{line: "cmd <> f", want: ErrRedirectionUnsupported, issue: IssueOperatorUnsupported},
		{line: "cmd 2>&file", want: ErrRedirectionUnsupported, issue: IssueOperatorUnsupported},
		{line: "cmd > f 2>> f", want: ErrRedirectionUnsupported, issue: IssueModeConflict},
		{line: `cmd > ""`, want: ErrRedirectionTargetRequired},
		{line: `cmd > ''`, want: ErrRedirectionTargetRequired},
		{line: "cmd > | x", want: ErrRedirectionTargetRequired},
		{line: "cat <<EOF", want: ErrShellMetacharacters},
	}
	for _, c := range cases {
		_, err := SplitElement(c.line)
		if !errors.Is(err, c.want) {
			t.Fatalf("SplitElement(%q) err = %v, want %v", c.line, err, c.want)
		}
		var redirection *RedirectionError
		if c.issue != "" && (!errors.As(err, &redirection) || redirection.Issue != c.issue) {
			t.Fatalf("SplitElement(%q) err = %v, want issue %s", c.line, err, c.issue)
		}
	}
}

func TestSplitElementRecordsGlobPatterns(t *testing.T) {
	cases := []struct {
		line  string
		globs []string
	}{
		{`ls *.go`, []string{"*.go"}},
		{`find . -name '*.go'`, nil},
		{`find . -name "*.go"`, nil},
		{`ls \*.go`, nil},
		{`ls src/'*'.go`, nil},
		{`ls 'src dir'/*.go`, []string{`src dir/*.go`}},
		{`ls a b?`, []string{"", "b?"}},
		{`ls "[x]"[ab]`, []string{`\[x\][ab]`}},
	}
	for _, c := range cases {
		el, err := SplitElement(c.line)
		if err != nil {
			t.Fatalf("SplitElement(%q): %v", c.line, err)
		}
		if !reflect.DeepEqual(el.Globs, c.globs) {
			t.Errorf("SplitElement(%q) globs = %q, want %q", c.line, el.Globs, c.globs)
		}
	}
}

func TestSplitElementMarksUnquotedAddresses(t *testing.T) {
	cases := []struct {
		line      string
		addressed []bool
	}{
		{`cat @scratch/a.txt`, []bool{true}},
		{`npm i @types/node`, []bool{false, true}},
		{`cat '@scratch/a.txt'`, nil},
		{`cat "@scratch/a.txt"`, nil},
		{`cat ""@scratch/a.txt`, nil},
		{`git log --author=@me x@y`, nil},
		{`ls`, nil},
	}
	for _, c := range cases {
		el, err := SplitElement(c.line)
		if err != nil {
			t.Fatalf("SplitElement(%q): %v", c.line, err)
		}
		if !reflect.DeepEqual(el.Addressed, c.addressed) {
			t.Errorf("SplitElement(%q) addressed = %v, want %v", c.line, el.Addressed, c.addressed)
		}
	}
}

func TestJoinAddressedCommandLineKeepsLiteralAddressesLiteral(t *testing.T) {
	line := JoinAddressedCommandLine(nil, "cat", []string{"@scratch/a", "@scratch/b", "plain"}, []bool{true, false, false})
	el, err := SplitElement(line)
	if err != nil {
		t.Fatalf("SplitElement(%q): %v", line, err)
	}
	if !reflect.DeepEqual(el.Args, []string{"@scratch/a", "@scratch/b", "plain"}) || !reflect.DeepEqual(el.Addressed, []bool{true, false, false}) {
		t.Fatalf("%q parsed to %q addressed %v", line, el.Args, el.Addressed)
	}
	if got := JoinCommandLine(nil, "npm", []string{"i", "@types/node"}); got != "npm i @types/node" {
		t.Fatalf("JoinCommandLine = %q, want the address unquoted", got)
	}
}

func TestEscapeGlobMatchesOnlyItself(t *testing.T) {
	if got := EscapeGlob(`/a[1]/b*?\c`); got != `/a\[1\]/b\*\?\\c` {
		t.Fatalf("EscapeGlob = %q", got)
	}
}

func TestRenderedRedirectionsParseBack(t *testing.T) {
	for _, line := range []string{
		"cmd > out 2>> err",
		"cmd 2>&1 > out",
		`cmd &> "build log"`,
		"cmd < in.txt",
		"cmd >> out",
	} {
		el, err := SplitElement(line)
		if err != nil {
			t.Fatalf("SplitElement(%q): %v", line, err)
		}
		rendered := JoinCommandLine(el.Env, el.Name, el.Args) + " " + RenderRedirects(el.Redirects)
		again, err := SplitElement(rendered)
		if err != nil {
			t.Fatalf("rendered %q no longer parses: %v", rendered, err)
		}
		if again.Streams != el.Streams || !reflect.DeepEqual(again.Redirects, el.Redirects) {
			t.Fatalf("%q rendered as %q parsed to %+v, want %+v", line, rendered, again.Streams, el.Streams)
		}
	}
}

func TestSplitCommandLineQuotedNewlineIsData(t *testing.T) {
	_, _, args, err := SplitCommandLine("git commit -m \"line one\nline two\"")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(args) != 3 || args[2] != "line one\nline two" {
		t.Errorf("args = %q", args)
	}
}

func TestJoinCommandLineRoundTrips(t *testing.T) {
	cases := [][]string{
		{"echo", "plain"},
		{"echo", "two words"},
		{"echo", `a"b`},
		{"echo", `a'b`},
		{"echo", `a\b`},
		{"echo", `a\"b`},
		{"echo", `a|b`, "c>d", "e<f", "g;h", "i&j", "$HOME", "`tick`"},
		{"echo", ""},
		{"echo", "tab\there"},
		{"git", "commit", "-m", "multi\nline body"},
		{"grep", `say "yes`, "file"},
		{"python3", "-c", `print("hi")`},
		{"echo", `back\slash "and quote`},
		{"ls", "*.go", "a?", "[x]", `\*`},
		{"A=1", "arg"},
	}
	for _, c := range cases {
		line := JoinCommandLine(nil, c[0], c[1:])
		env, name, args, err := SplitCommandLine(line)
		if err != nil {
			t.Fatalf("round-trip %q → %q: %v", c, line, err)
		}
		if len(env) != 0 {
			t.Errorf("round-trip unexpected env: %v", env)
		}
		got := append([]string{name}, args...)
		if !reflect.DeepEqual(got, c) {
			t.Errorf("round-trip %q → %q → %q", c, line, got)
		}
	}
}

func TestJoinCommandLineWithEnvRoundTrips(t *testing.T) {
	cases := []struct {
		env  map[string]string
		name string
		args []string
	}{
		{
			env:  map[string]string{"PORT": "8080"},
			name: "go",
			args: []string{"run", "."},
		},
		{
			env:  map[string]string{"A": "1", "B": "two words", "C": ""},
			name: "./script",
			args: []string{"--flag", "val"},
		},
	}
	for _, c := range cases {
		line := JoinCommandLine(c.env, c.name, c.args)
		env, name, args, err := SplitCommandLine(line)
		if err != nil {
			t.Fatalf("round-trip with env %q: %v", line, err)
		}
		if !reflect.DeepEqual(env, c.env) {
			t.Errorf("round-trip env = %v, want %v", env, c.env)
		}
		if name != c.name {
			t.Errorf("round-trip name = %q, want %q", name, c.name)
		}
		if !reflect.DeepEqual(args, c.args) {
			t.Errorf("round-trip args = %v, want %v", args, c.args)
		}
	}
}
