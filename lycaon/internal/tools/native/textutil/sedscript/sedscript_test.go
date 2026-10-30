package sedscript

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textsearch"
)

type engine struct{ posix textsearch.POSIXEngine }

func (e engine) Compile(ctx context.Context, pattern string, extended, fold bool) (Regex, error) {
	if fold {
		return e.posix.CompileFolded(ctx, pattern, extended)
	}
	return e.posix.Compile(ctx, pattern, extended)
}

// lines is an Input over one named text.
type lines struct {
	name, text string
	delimiter  byte
}

func (l *lines) Next() (Record, error) {
	if l.text == "" {
		return Record{}, io.EOF
	}
	i := strings.IndexByte(l.text, l.delimiter)
	if i < 0 {
		record := Record{Text: []byte(l.text), Source: l.name}
		l.text = ""
		return record, nil
	}
	record := Record{Text: []byte(l.text[:i]), Terminated: true, Source: l.name}
	l.text = l.text[i+1:]
	return record, nil
}

type fixture struct {
	script   string
	extended bool
	quiet    bool
	nul      bool
	files    map[string][]byte
	inputs   []string
}

// run parses and runs a script over each input as one stream (or one stream
// per input with separate) and returns stdout, every write file and status.
func (f fixture) run(t *testing.T, separate bool) (string, map[string]*bytes.Buffer, int, error) {
	t.Helper()
	posix, err := textsearch.NewPOSIXEngine()
	testutil.FailErr(t, "create sed POSIX engine", err)
	program, err := Parse(t.Context(), f.script, f.extended, engine{posix})
	if err != nil {
		return "", nil, 0, err
	}
	delimiter := byte('\n')
	if f.nul {
		delimiter = 0
	}
	var stdout bytes.Buffer
	writers := map[string]io.Writer{}
	buffers := map[string]*bytes.Buffer{}
	for _, name := range program.WriteFiles() {
		buffers[name] = &bytes.Buffer{}
		writers[name] = buffers[name]
	}
	m, err := NewMachine(t.Context(), program, Config{Quiet: f.quiet || program.Quiet(), Delimiter: delimiter, SpaceLimit: 1 << 20, StepLimit: 1000, Stdout: &stdout, Writers: writers, Files: f.files})
	testutil.FailErr(t, "create bounded sed machine", err)
	if separate {
		for i, input := range f.inputs {
			if quit, err := m.Stream(&lines{name: "f" + string(rune('0'+i)), text: input, delimiter: delimiter}, nil); err != nil || quit {
				return stdout.String(), buffers, m.Status(), err
			}
		}
		return stdout.String(), buffers, m.Status(), nil
	}
	_, err = m.Stream(&lines{name: "-", text: strings.Join(f.inputs, ""), delimiter: delimiter}, nil)
	return stdout.String(), buffers, m.Status(), err
}

func TestScriptsMatchGNUSed(t *testing.T) {
	abcd := "a\nb\nc\nd\n"
	for _, c := range []struct {
		name, script, input, want string
		quiet, extended           bool
	}{
		{"join all lines", `:a;N;$!ba;s/\n/,/g`, abcd, "a,b,c,d\n", false, false},
		{"reverse with hold space", `1!G;h;$!d`, abcd, "d\nc\nb\na\n", false, false},
		{"swap pairs", `N;s/\(.*\)\n\(.*\)/\2\n\1/`, abcd, "b\na\nd\nc\n", false, false},
		{"exchange", `x;$!d;x;G`, "a\nb\n", "b\na\n", false, false},
		{"print through first newline then delete", `$!N;P;D`, "a\na\nb\nb\nc\n", "a\na\nb\nb\nc\n", false, false},
		{"uniq idiom", `$!N;/^\(.*\)\n\1$/!P;D`, "a\na\nb\nc\nc\n", "a\nb\nc\n", false, false},
		{"tac idiom", `1!G;h;$p`, "1\n2\n3\n", "3\n2\n1\n", true, false},
		{"t loops until no change", `:x;s/ab/ba/;tx`, "aabb\n", "bbaa\n", false, false},
		{"T branches when nothing replaced", `s/x/y/;Tend;s/$/!/;:end`, "x\nz\n", "y!\nz\n", false, false},
		{"n prints and reads", `n;d`, "1\n2\n3\n", "1\n3\n", false, false},
		{"N at the end prints", `N;s/\n/-/`, "1\n2\n3\n", "1-2\n3\n", false, false},
		{"y transliterates scalars", `y/abcé/ABCÉ/`, "abcé x\n", "ABCÉ x\n", false, false},
		{"line numbers", `=`, "x\ny\n", "1\nx\n2\ny\n", false, false},
		{"last line number", `$=`, abcd, "4\n", true, false},
		{"l escapes and wraps", `l 6`, "a\tb\\c\x01\n", "a\\tb\\\n\\\\c\\\n\\001$\n", true, false},
		{"l default", `l`, "\xc3\xa9\n", "\\303\\251$\n", true, false},
		{"step address", `0~2d`, "1\n2\n3\n4\n5\n", "1\n3\n5\n", false, false},
		{"first step address", `2~3!d`, "1\n2\n3\n4\n5\n", "2\n5\n", false, false},
		{"relative range", `/b/,+1d`, abcd, "a\nd\n", false, false},
		{"multiple range", `2,~4d`, "1\n2\n3\n4\n5\n", "1\n5\n", false, false},
		{"zero address range", `0,/a/d`, "a\nb\na\n", "b\na\n", false, false},
		{"custom delimiter address", `\,b,d`, abcd, "a\nc\nd\n", false, false},
		{"case-insensitive address", `/B/Id`, abcd, "a\nc\nd\n", false, false},
		{"case-insensitive substitution", `s/A/x/Ig`, "aAa\n", "xxx\n", false, false},
		{"empty regex reuses the last", `/b/s//X/`, abcd, "a\nX\nc\nd\n", false, false},
		{"case conversion", `s/\(\w*\) \(\w*\)/\U\1\E \u\2/`, "hello world\n", "HELLO World\n", false, false},
		{"lower case one", `s/.*/\L\u&/`, "hELLO\n", "Hello\n", false, false},
		{"whole match as \\0", `s/b/[\0]/`, "abc\n", "a[b]c\n", false, false},
		{"replacement escapes", `s/x/\t\x41\o102\d067\cA/`, "x\n", "\tABC\x01\n", false, false},
		{"Q quits silently", `2Q`, abcd, "a\n", false, false},
		{"q prints and quits", `2q`, abcd, "a\nb\n", false, false},
		{"z empties", `2z`, abcd, "a\n\nc\nd\n", false, false},
		{"P prints the first line", `N;P;d`, "a\nb\n", "a\n", false, false},
		{"append and insert in range", `2,3a\
+
2i -`, "1\n2\n3\n4\n", "1\n-\n2\n+\n3\n+\n4\n", false, false},
		{"comment and #n", "#n\n2p # print", abcd, "b\n", false, false},
		{"braces without separators", `/b/{s/b/B/;p}`, abcd, "a\nB\nB\nc\nd\n", false, false},
		{"extended", `s/(a|b)+/<&>/g`, "aab c\n", "<aab> c\n", false, true},
		{"missing final newline kept", `p`, "x", "x\nx", false, false},
		{"G adds a newline", `G`, "a\nb\n", "a\n\nb\n\n", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, _, _, err := fixture{script: c.script, quiet: c.quiet, extended: c.extended, inputs: []string{c.input}}.run(t, false)
			if err != nil || got != c.want {
				t.Fatalf("sed %q: %q, %v; want %q", c.script, got, err, c.want)
			}
		})
	}
}

func TestFilesExitCodesAndSeparateStreams(t *testing.T) {
	got, writes, status, err := fixture{script: "/a/w out\ns/b/B/w /dev/stdout\n3q7", inputs: []string{"a\nb\nc\nd\n"}}.run(t, false)
	if err != nil || got != "a\nB\nB\nc\n" || writes["out"].String() != "a\n" || status != 7 {
		t.Fatalf("w and q: %q %v %d %v", got, writes, status, err)
	}
	files := map[string][]byte{"ins": []byte("X\n"), "lines": []byte("1\n2")}
	got, _, _, err = fixture{script: "1r ins\nR lines\n$r missing", files: files, inputs: []string{"a\nb\nc\n"}}.run(t, false)
	if err != nil || got != "a\nX\n1\nb\n2\nc\n" {
		t.Fatalf("r and R: %q %v", got, err)
	}
	got, _, _, err = fixture{script: "$!d;F", inputs: []string{"a\nb\n", "c\n"}}.run(t, true)
	if err != nil || got != "f0\nb\nf1\nc\n" {
		t.Fatalf("separate streams: %q %v", got, err)
	}
	got, _, _, err = fixture{script: "x", inputs: []string{"a\nb\n", "c\n"}}.run(t, true)
	if err != nil || got != "\na\nb\n" {
		t.Fatalf("hold space persists across files: %q %v", got, err)
	}
	got, _, _, err = fixture{script: "N;s/b/B/", nul: true, inputs: []string{"a\x00b\x00"}}.run(t, false)
	if err != nil || got != "a\x00B\x00" {
		t.Fatalf("NUL records: %q %v", got, err)
	}
}

func TestRefusalsAndLimits(t *testing.T) {
	for _, c := range []struct {
		script      string
		unsupported bool
	}{
		{"e date", true}, {"s/a/b/e", true}, {"s/a/b/M", true}, {"/a/Mp", true},
		{"s/a/b", false}, {`s/a/\1/`, false}, {"{p", false}, {"}", false}, {"pq", false}, {"0d", false},
		{"b nowhere", false}, {":a;:a", false}, {"y/ab/c/", false}, {"1,2q", false}, {"r", false}, {"a", false}, {"k", false},
	} {
		_, _, _, err := fixture{script: c.script, inputs: []string{"a\n"}}.run(t, false)
		var script *ScriptError
		if !errors.As(err, &script) || script.Unsupported != c.unsupported {
			t.Fatalf("%q: %v; want script error unsupported=%v", c.script, err, c.unsupported)
		}
	}
	var limit *LimitError
	if _, _, _, err := (fixture{script: ":a;ba", inputs: []string{"a\n"}}).run(t, false); !errors.As(err, &limit) || limit.Resource != "query_work_steps" {
		t.Fatalf("endless branch: %v", err)
	}
	if _, _, _, err := (fixture{script: "G;D", inputs: []string{"a\n"}}).run(t, false); !errors.As(err, &limit) {
		t.Fatalf("endless restart: %v", err)
	}
	var runtime *RuntimeError
	if _, _, _, err := (fixture{script: "s//x/", inputs: []string{"a\n"}}).run(t, false); !errors.As(err, &runtime) {
		t.Fatalf("empty regex first: %v", err)
	}
}
