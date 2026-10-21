package query

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var queryFiles = map[string]string{
	"doc.json":   `{"b":{},"a":[1,2],"name":"svc"}` + "\n",
	"two.json":   "{\"a\":1}\n{\"a\":2}\n",
	"cfg.yaml":   "# settings\nserver:\n  port: 8080\n  hosts: [a, b]\nname: api\n",
	"multi.yaml": "kind: A\n---\nkind: B\n",
	"cfg.toml":   "[package]\nname = \"demo\"\nedition = 2021\n",
	"bad.json":   "1 {",
}

type queryCase struct {
	argv, stdin, stdout string
	status              int
	stderr              bool
}

var queryCases = []queryCase{
	{argv: "jq .x", stdin: `{"x":1}` + "\n", stdout: "1\n"},
	{argv: "jq -r .x", stdin: `{"x":1}` + "\n", stdout: "1\n"},
	{argv: "jq .a", stdin: "{\"a\":1}\n{\"a\":2}", stdout: "1\n2\n"},
	{argv: "jq . doc.json", stdout: "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": {},\n  \"name\": \"svc\"\n}\n"},
	{argv: "jq -c . doc.json", stdout: `{"a":[1,2],"b":{},"name":"svc"}` + "\n"},
	{argv: "jq --compact-output .a two.json doc.json", stdout: "1\n2\n[1,2]\n"},
	{argv: "jq -r .name doc.json", stdout: "svc\n"},
	{argv: "jq --raw-output .a doc.json", stdout: "[\n  1,\n  2\n]\n"},
	{argv: "jq .name doc.json", stdout: "\"svc\"\n"},
	{argv: "jq -r .", stdin: `"tab\there" "quote\"" "é"`, stdout: "tab\there\nquote\"\né\n"},
	{argv: "jq -c .", stdin: `"\u007f<&>" "\u0001"`, stdout: "\"\\u007f<&>\"\n\"\\u0001\"\n"},
	{argv: "jq -s -c .", stdin: "1 2 3", stdout: "[1,2,3]\n"},
	{argv: "jq --slurp -c .", stdin: "", stdout: "[]\n"},
	{argv: "jq -n 1+1", stdout: "2\n"},
	{argv: "jq --null-input -c [inputs]", stdin: "1 2", stdout: "[1,2]\n"},
	{argv: "jq -c [.,input]", stdin: "1 2 3 4", stdout: "[1,2]\n[3,4]\n"},
	{argv: "jq -n -c --arg x v --argjson y {\"z\":1} [$x,$y]", stdout: "[\"v\",{\"z\":1}]\n"},
	{argv: "jq -n --arg x one --arg x two $x", stdout: "\"two\"\n"},
	{argv: "jq -c .", stdin: "1.5 2.0 100000000000000000000 1e3 -0.25 3.14159", stdout: "1.5\n2\n100000000000000000000\n1000\n-0.25\n3.14159\n"},
	{argv: "jq -c .[]|.*2", stdin: "[0.1,1.25,7]", stdout: "0.2\n2.5\n14\n"},
	{argv: "jq -e .", stdin: "true", stdout: "true\n"},
	{argv: "jq -e .", stdin: "false", stdout: "false\n", status: 1},
	{argv: "jq --exit-status .", stdin: "null", stdout: "null\n", status: 1},
	{argv: "jq -e .[]", stdin: "[]", stdout: "", status: 4},
	{argv: "jq -e .[]", stdin: "[1,null]", stdout: "1\nnull\n", status: 1},
	{argv: "jq .", stdin: "1 {", stdout: "1\n", status: 2, stderr: true},
	{argv: "jq . bad.json", stdout: "1\n", status: 2, stderr: true},
	{argv: "jq .x", stdin: `"a" {"x":3}`, stdout: "3\n", status: 2, stderr: true},
	{argv: "jq .a missing.json two.json", stdout: "1\n2\n", status: 2, stderr: true},
	{argv: "jq -n $ENV|length", stdout: "0\n"},
	{argv: "jq -n -c env", stdout: "{}\n"},
	{argv: "jq -n 1,halt,2", stdout: "1\n"},
	{argv: "jq -n \"bye\\n\"|halt_error(3)", status: 3, stderr: true},
	{argv: "yq .server.port cfg.yaml", stdout: "8080\n"},
	{argv: "yq .server cfg.yaml", stdout: "hosts:\n  - a\n  - b\nport: 8080\n"},
	{argv: "yq -c .server cfg.yaml", stdout: `{"hosts":["a","b"],"port":8080}` + "\n"},
	{argv: "yq .kind multi.yaml", stdout: "A\nB\n"},
	{argv: "yq . multi.yaml", stdout: "kind: A\n---\nkind: B\n"},
	{argv: "yq .package.edition cfg.toml", stdout: "2021\n"},
	{argv: "yq -r .package.name cfg.toml", stdout: "demo\n"},
	{argv: "yq -r .x", stdin: "x: hello\n", stdout: "hello\n"},
	{argv: "yq .x", stdin: "x: hello\n", stdout: "hello\n"},
	{argv: "yq .", stdin: "a: '1'\nb: true\n", stdout: "a: \"1\"\nb: true\n"},
	{argv: "yq -r .x", stdin: `{"x":1}` + "\n", stdout: "1\n"},
	{argv: "yq -e .missing", stdin: "a: 1\n", stdout: "null\n", status: 1},
}

// TestQueryFormsProduceExactBytes runs each catalogued jq and yq form over
// stdin and captured files and compares stdout and status byte for byte.
func TestQueryFormsProduceExactBytes(t *testing.T) {
	h := newHarness(t, queryFiles)
	for _, c := range queryCases {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec(c.stdin, splitWords(c.argv)...)
			r.expect(t, c.argv, c.stdout, c.status)
			if c.stderr != (r.stderr != "") {
				t.Fatalf("stderr %q, want stderr=%v", r.stderr, c.stderr)
			}
		})
	}
}

// splitWords splits a fixture argv on spaces; queries in fixtures contain none.
func splitWords(argv string) []string { return strings.Fields(argv) }

// TestQueryReadsPinTheirSources: every file a query reads is pinned before
// evaluation; stdin pins nothing.
func TestQueryReadsPinTheirSources(t *testing.T) {
	h := newHarness(t, queryFiles)
	r := h.exec("", "jq", "-c", ".", "two.json", "doc.json")
	pins := h.pins.pins[r.execution]
	if len(pins) != 2 || pins[0].Path != "two.json" || pins[1].Path != "doc.json" || pins[0].ContentSHA256 != textfile.SHA256([]byte(queryFiles["two.json"])) || pins[0].Role != toolcontract.SourceRevision {
		t.Fatalf("pins = %+v", pins)
	}
	stdin := h.exec("1", "jq", ".")
	if len(h.pins.pins[stdin.execution]) != 0 {
		t.Fatal("stdin pinned")
	}
	// An unread trailing file is never captured.
	halted := h.exec("", "jq", "-n", "input|halt", "two.json", "doc.json")
	if got := h.pins.pins[halted.execution]; len(got) != 1 {
		t.Fatalf("lazy capture pinned %+v", got)
	}
}

// TestQueryBudgetsRefuse: depth, outputs, value size and evaluation time are
// bounded at the adapter; nothing is truncated into a successful result.
func TestQueryBudgetsRefuse(t *testing.T) {
	h := newHarness(t, nil)
	deep := strings.Repeat("[", 129) + strings.Repeat("]", 129)
	h.exec(deep, "jq", ".").refused(t, "deep input", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "jq", "-n", "range(10001)").refused(t, "too many outputs", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "jq", "-n", "[range(3000000)]|tostring").refused(t, "oversized value", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "jq", "-n", "last(range(1e15))").refused(t, "evaluation time", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "jq", "-n", "import \"lib\" as lib; 1").refused(t, "module loader", "JQ_QUERY_INVALID")
	h.exec("", "jq", ".[").refused(t, "compile error", "JQ_QUERY_INVALID")
	h.exec("", "jq").refused(t, "missing query", "COMMAND_OPERAND_INVALID")
	h.exec("", "jq", "-n", "--argjson", "x", "{bad", "$x").refused(t, "bad argjson", "COMMAND_OPERAND_INVALID")
	h.exec("", "jq", "--sort-keys", ".").refused(t, "unsupported option", "COMMAND_OPTION_UNSUPPORTED")
	h.owners.limitOverride = map[string]int64{"stream_bytes": 4}
	h.exec("", "jq", "-n", "\"long string\"").refused(t, "stream bound", "COMMAND_LIMIT_EXCEEDED")
}
