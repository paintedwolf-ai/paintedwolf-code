package toolcommand

import (
	"reflect"
	"testing"
)

func TestInspectCommandFileWriteBuildsExactCalls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tool string
		args map[string]any
		want []ReplacementCall
		path string
		app  bool
	}{
		{
			name: "quoted heredoc after redirect",
			tool: "command",
			args: map[string]any{"command": "cat > index.html << 'EOF'\n<!DOCTYPE html>\n<html>$HOME</html>\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "index.html", "content": "<!DOCTYPE html>\n<html>$HOME</html>\n"}}},
			path: "index.html",
		},
		{
			name: "quoted heredoc before redirect",
			tool: "command",
			args: map[string]any{"command": "cat << 'EOF' > src/app.js\nconsole.log('ready');\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "src/app.js", "content": "console.log('ready');\n"}}},
			path: "src/app.js",
		},
		{
			name: "leading redirect before command name",
			tool: "command",
			args: map[string]any{"command": "1> notes.txt cat << \"EOF\"\nline 1\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "notes.txt", "content": "line 1\n"}}},
			path: "notes.txt",
		},
		{
			name: "unquoted heredoc without shell expansion",
			tool: "command",
			args: map[string]any{"command": "cat << EOF > plain.txt\nhello world\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "plain.txt", "content": "hello world\n"}}},
			path: "plain.txt",
		},
		{
			name: "tab-stripped heredoc with append redirect",
			tool: "command",
			args: map[string]any{"command": "cat <<- 'EOF' >> notes.txt\n\tfirst\n\t  indented\n\tEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "notes.txt", "content": "first\n  indented\n", "append": true}}},
			path: "notes.txt",
			app:  true,
		},
		{
			name: "herestring to file",
			tool: "command",
			args: map[string]any{"command": "cat <<< 'single line' > hello.txt"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "hello.txt", "content": "single line\n"}}},
			path: "hello.txt",
		},
		{
			name: "tee with heredoc and quiet devnull redirect",
			tool: "command",
			args: map[string]any{"command": "tee -a log.txt << 'EOF' > /dev/null\nentry\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "log.txt", "content": "entry\n", "append": true}}},
			path: "log.txt",
			app:  true,
		},
		{
			name: "cat heredoc piped to tee",
			tool: "command",
			args: map[string]any{"command": "cat << 'EOF' | tee index.html\n<h1>Piped</h1>\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "index.html", "content": "<h1>Piped</h1>\n"}}},
			path: "index.html",
		},
		{
			name: "dd of=file with heredoc",
			tool: "command",
			args: map[string]any{"command": "dd status=none of=out.txt << 'EOF'\npayload\nEOF"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "out.txt", "content": "payload\n"}}},
			path: "out.txt",
		},
		{
			name: "subshell heredoc redirected to file",
			tool: "command",
			args: map[string]any{"command": "(cat << 'EOF'\nsubshell body\nEOF\n) > sub.txt"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "sub.txt", "content": "subshell body\n"}}},
			path: "sub.txt",
		},
		{
			name: "sh -c wrapped heredoc",
			tool: "command",
			args: map[string]any{"command": "sh -c \"cat << 'EOF' > wrapped.html\n<p>ok</p>\nEOF\""},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "wrapped.html", "content": "<p>ok</p>\n"}}},
			path: "wrapped.html",
		},
		{
			name: "bash -lc wrapped printf redirect",
			tool: "command",
			args: map[string]any{"command": "bash -lc 'printf \"%s\\n\" \"hello\" > greet.txt'"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "greet.txt", "content": "hello\n"}}},
			path: "greet.txt",
		},
		{
			name: "bash script heredoc containing inner heredoc file write",
			tool: "command",
			args: map[string]any{"command": "bash << 'SCRIPT'\ncat << 'HTML' > page.html\n<main>Hi</main>\nHTML\nSCRIPT"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "page.html", "content": "<main>Hi</main>\n"}}},
			path: "page.html",
		},
		{
			name: "mkdir -p followed by heredoc file write with cwd",
			tool: "command",
			args: map[string]any{
				"command": "mkdir -p assets && cat << 'EOF' > assets/app.css\nbody { margin: 0; }\nEOF",
				"cwd":     "web",
			},
			want: []ReplacementCall{
				{Tool: "mkdir", Args: map[string]any{"paths": []any{"web/assets"}}},
				{Tool: "write", Args: map[string]any{"path": "web/assets/app.css", "content": "body { margin: 0; }\n"}},
			},
			path: "web/assets/app.css",
		},
		{
			name: "structured stdin with cat redirect",
			tool: "command",
			args: map[string]any{"command": "cat > direct.txt", "stdin": "from stdin\n"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "direct.txt", "content": "from stdin\n"}}},
			path: "direct.txt",
		},
		{
			name: "structured stdin and stdout_to with append",
			tool: "command",
			args: map[string]any{"command": "cat", "stdin": "more\n", "stdout_to": "direct.txt", "append": true},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "direct.txt", "content": "more\n", "append": true}}},
			path: "direct.txt",
			app:  true,
		},
		{
			name: "structured pipeline array with stdin and tee",
			tool: "command",
			args: map[string]any{"pipeline": []any{"cat", "tee -a pipe.txt"}, "stdin": "piped\n"},
			want: []ReplacementCall{{Tool: "write", Args: map[string]any{"path": "pipe.txt", "content": "piped\n", "append": true}}},
			path: "pipe.txt",
			app:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := InspectCommandFileWrite(t.Context(), tc.tool, tc.args, t.TempDir())
			if !ok {
				t.Fatalf("InspectCommandFileWrite(%#v) did not match", tc.args)
			}
			if !got.Exact || !reflect.DeepEqual(got.Calls, tc.want) {
				t.Fatalf("Exact=%v Calls=%#v, want Exact=true Calls=%#v", got.Exact, got.Calls, tc.want)
			}
			if got.Path != tc.path || got.Append != tc.app {
				t.Fatalf("Path=%q Append=%v, want Path=%q Append=%v", got.Path, got.Append, tc.path, tc.app)
			}
		})
	}
}

func TestInspectCommandFileWriteDetectsInexactShellWrites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tool string
		args map[string]any
		path string
	}{
		{
			name: "single-line unterminated heredoc",
			tool: "command",
			args: map[string]any{"command": "cat <<'EOF' > index.html"},
			path: "index.html",
		},
		{
			name: "unquoted heredoc with shell variable expansion",
			tool: "command",
			args: map[string]any{"command": "cat << EOF > config.env\nUSER=$USER\nEOF"},
			path: "config.env",
		},
		{
			name: "python heredoc redirecting stdout to file",
			tool: "command",
			args: map[string]any{"command": "python3 - << 'EOF' > generated.json\nprint('{}')\nEOF"},
			path: "generated.json",
		},
		{
			name: "terminal_open heredoc to file",
			tool: "terminal_open",
			args: map[string]any{"command": "cat << 'EOF' > index.html\n<h1>Hi</h1>\nEOF"},
			path: "index.html",
		},
		{
			name: "for loop with heredoc file write parsed via tree-sitter bash",
			tool: "command",
			args: map[string]any{"command": "for f in a.txt; do\ncat << 'EOF' > out.txt\nhi\nEOF\ndone"},
			path: "out.txt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := InspectCommandFileWrite(t.Context(), tc.tool, tc.args, t.TempDir())
			if !ok {
				t.Fatalf("InspectCommandFileWrite(%#v) did not match", tc.args)
			}
			if got.Exact || len(got.Calls) != 0 {
				t.Fatalf("expected inexact match without replacement_calls, got Exact=%v Calls=%#v", got.Exact, got.Calls)
			}
			if got.Path != tc.path {
				t.Fatalf("Path=%q, want %q", got.Path, tc.path)
			}
		})
	}
}

func TestInspectCommandFileWriteIgnoresNonWriteCommands(t *testing.T) {
	t.Parallel()
	cases := []map[string]any{
		{"command": "python3 - << 'EOF'\nprint('hello')\nEOF"},
		{"command": "sqlite3 app.db << 'EOF'\nSELECT 1;\nEOF"},
		{"command": "cat << 'EOF' > /dev/null\nignored\nEOF"},
		{"command": "ls > listing.txt"},
		{"command": "cat notes.txt > copy.txt"},
		{"command": "sort < in.txt > out.log"},
		{"command": `sh -c "echo broken >&2" 2> err.log`},
		{"command": `sh -c "echo new out; echo new err >&2" > out 2>> err`},
		{"command": "tool --check | tee output.txt"},
		{"command": "python3 transform.py > out.txt", "stdin": "raw input\n"},
	}

	for _, args := range cases {
		if got, ok := InspectCommandFileWrite(t.Context(), "command", args, t.TempDir()); ok {
			t.Fatalf("non-write command %#v falsely matched USE_WRITE_NATIVE: %+v", args, got)
		}
	}
}
