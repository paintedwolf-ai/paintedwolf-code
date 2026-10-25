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
	"bad.json":   "1 {",
	"nums.json":  "[1.0, 2.50, 1e3, 100000000000000000001, -0]\n",
	"lines.txt":  "alpha\nbeta\n\ngamma",
	"vals.json":  "1 [2] {\"c\":3}\n",
	"uni.json":   "{\"é\":\"😀\",\"b\":[]}\n",
	"prog.jq":    ".a\n",
	"cfg.yaml":   "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\nname: api\n",
	"multi.yaml": "kind: A\n---\nkind: B\n",
	"cfg.toml":   "[package]\nname = \"demo\"\nedition = 2021\n",
}

type queryCase struct {
	argv, stdin, stdout, stderr string
	status                      int
}

// jqCases are jq 1.8.1's own stdout, stderr and status for each form,
// recorded from the jq binary over the same files and stdin.
var jqCases = []queryCase{
	{argv: "jq", stdin: "{\"x\":1}", stdout: "{\n  \"x\": 1\n}\n"},
	{argv: "jq -c", stdin: "{\"b\":1,\"a\":2}", stdout: "{\"b\":1,\"a\":2}\n"},
	{argv: "jq -n -c $ARGS --args a b", stdout: "{\"positional\":[\"a\",\"b\"],\"named\":{}}\n"},
	{argv: "jq -n -c $ARGS --jsonargs 1 {\"a\":2}", stdout: "{\"positional\":[1,{\"a\":2}],\"named\":{}}\n"},
	{argv: "jq -n -c --arg k v --argjson j [1] $ARGS", stdout: "{\"positional\":[],\"named\":{\"k\":\"v\",\"j\":[1]}}\n"},
	{argv: "jq -f prog.jq two.json", stdout: "1\n2\n"},
	{argv: "jq --from-file prog.jq", stdin: "{\"a\":5}", stdout: "5\n"},
	{argv: "jq -f missing.jq", stdin: "1", stdout: "", status: 2, stderr: "jq: Could not open missing.jq: No such file or directory\n"},
	{argv: "jq --raw-output0 .[]", stdin: "[\"a\",\"b\"]", stdout: "a\u0000b\u0000"},
	{argv: "jq -c --stream .", stdin: "[1,{\"a\":2}]", stdout: "[[0],1]\n[[1,\"a\"],2]\n[[1,\"a\"]]\n[[1]]\n"},
	{argv: "jq -n --argjson x {bad $x", stdout: "", status: 2, stderr: "jq: invalid JSON text passed to --argjson\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n"},
	{argv: "jq --indent 9 .", stdin: "1", stdout: "", status: 2, stderr: "jq: --indent takes a number between -1 and 7\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n"},
	{argv: "jq -c . --args x", stdin: "{\"a\":1}", stdout: "{\"a\":1}\n"},
	{argv: "jq -n -c --slurpfile v bad.json $v", stdout: "", status: 2, stderr: "jq: Bad JSON in --slurpfile v bad.json: Unfinished JSON term at EOF at line 1, column 3\n"},
	{argv: "jq .x", stdin: "{\"x\":1}\n", stdout: "1\n"},
	{argv: "jq -r .x", stdin: "{\"x\":1}\n", stdout: "1\n"},
	{argv: "jq .a", stdin: "{\"a\":1}\n{\"a\":2}", stdout: "1\n2\n"},
	{argv: "jq . doc.json", stdout: "{\n  \"b\": {},\n  \"a\": [\n    1,\n    2\n  ],\n  \"name\": \"svc\"\n}\n"},
	{argv: "jq -c . doc.json", stdout: "{\"b\":{},\"a\":[1,2],\"name\":\"svc\"}\n"},
	{argv: "jq --compact-output .a two.json doc.json", stdout: "1\n2\n[1,2]\n"},
	{argv: "jq -r .name doc.json", stdout: "svc\n"},
	{argv: "jq --raw-output .a doc.json", stdout: "[\n  1,\n  2\n]\n"},
	{argv: "jq -r .", stdin: "\"tab\\there\" \"quote\\\"\" \"é\"", stdout: "tab\there\nquote\"\né\n"},
	{argv: "jq -c .", stdin: "\"\\u007f<&>\" \"\\u0001\"", stdout: "\"\\u007f<&>\"\n\"\\u0001\"\n"},
	{argv: "jq -s -c .", stdin: "1 2 3", stdout: "[1,2,3]\n"},
	{argv: "jq --slurp -c .", stdout: "[]\n"},
	{argv: "jq -n 1+1", stdout: "2\n"},
	{argv: "jq --null-input -c [inputs]", stdin: "1 2", stdout: "[1,2]\n"},
	{argv: "jq -c [.,input]", stdin: "1 2 3 4", stdout: "[1,2]\n[3,4]\n"},
	{argv: "jq -n -c --arg x v --argjson y {\"z\":1} [$x,$y]", stdout: "[\"v\",{\"z\":1}]\n"},
	{argv: "jq -n --arg x one --arg x two $x", stdout: "\"one\"\n"},
	{argv: "jq -c .", stdin: "1.5 2.0 100000000000000000000 1e3 -0.25 3.14159", stdout: "1.5\n2.0\n100000000000000000000\n1E+3\n-0.25\n3.14159\n"},
	{argv: "jq -c . nums.json", stdout: "[1.0,2.50,1E+3,100000000000000000001,-0]\n"},
	{argv: "jq -c .[]|.*2", stdin: "[0.1,1.25,7]", stdout: "0.2\n2.5\n14\n"},
	{argv: "jq -c map(.+0) nums.json", stdout: "[1,2.5,1000,1e+20,0]\n"},
	{argv: "jq -e .", stdin: "true", stdout: "true\n"},
	{argv: "jq -e .", stdin: "false", stdout: "false\n", status: 1},
	{argv: "jq --exit-status .", stdin: "null", stdout: "null\n", status: 1},
	{argv: "jq -e .[]", stdin: "[]", stdout: "", status: 4},
	{argv: "jq -e .[]", stdin: "[1,null]", stdout: "1\nnull\n", status: 1},
	{argv: "jq .", stdin: "1 {", stdout: "1\n", status: 5, stderr: "jq: parse error: Unfinished JSON term at EOF at line 1, column 3\n"},
	{argv: "jq . bad.json", stdout: "1\n", status: 5, stderr: "jq: parse error: Unfinished JSON term at EOF at line 1, column 3\n"},
	{argv: "jq .", stdin: "{\"a\":\n", stdout: "", status: 5, stderr: "jq: parse error: Unfinished JSON term at EOF at line 2, column 0\n"},
	{argv: "jq .x", stdin: "\"a\" {\"x\":3}", stdout: "3\n", stderr: "jq: error (at <stdin>:0): Cannot index string with string \"x\"\n"},
	{argv: "jq .x", stdin: "{\"x\":3} \"a\"", stdout: "3\n", status: 5, stderr: "jq: error (at <stdin>:0): Cannot index string with string \"x\"\n"},
	{argv: "jq .a missing.json two.json", stdout: "1\n", status: 2, stderr: "jq: error: Could not open file missing.json: No such file or directory\n"},
	{argv: "jq .a two.json missing.json", stdout: "1\n2\n", status: 2, stderr: "jq: error: Could not open file missing.json: No such file or directory\n"},
	{argv: "jq -n 1,halt,2", stdout: "1\n"},
	{argv: "jq -n \"bye\\n\"|halt_error(3)", stdout: "", status: 3, stderr: "bye\n"},
	{argv: "jq -n {\"a\":1}|halt_error", stdout: "", status: 5, stderr: "{\"a\":1}\n"},
	{argv: "jq -c keys,keys_unsorted,to_entries doc.json", stdout: "[\"a\",\"b\",\"name\"]\n[\"b\",\"a\",\"name\"]\n[{\"key\":\"b\",\"value\":{}},{\"key\":\"a\",\"value\":[1,2]},{\"key\":\"name\",\"value\":\"svc\"}]\n"},
	{argv: "jq -c .c=1|del(.b) doc.json", stdout: "{\"a\":[1,2],\"name\":\"svc\",\"c\":1}\n"},
	{argv: "jq -S . doc.json", stdout: "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": {},\n  \"name\": \"svc\"\n}\n"},
	{argv: "jq -S -c . doc.json", stdout: "{\"a\":[1,2],\"b\":{},\"name\":\"svc\"}\n"},
	{argv: "jq --tab . doc.json", stdout: "{\n\t\"b\": {},\n\t\"a\": [\n\t\t1,\n\t\t2\n\t],\n\t\"name\": \"svc\"\n}\n"},
	{argv: "jq --indent 1 .a doc.json", stdout: "[\n 1,\n 2\n]\n"},
	{argv: "jq --indent 0 . doc.json", stdout: "{\n\"b\": {},\n\"a\": [\n1,\n2\n],\n\"name\": \"svc\"\n}\n"},
	{argv: "jq --tab --indent 3 .a doc.json", stdout: "[\n   1,\n   2\n]\n"},
	{argv: "jq --indent 3 -c .a doc.json", stdout: "[1,2]\n"},
	{argv: "jq -c --indent 3 .a doc.json", stdout: "[\n   1,\n   2\n]\n"},
	{argv: "jq -a -c . uni.json", stdout: "{\"\\u00e9\":\"\\ud83d\\ude00\",\"b\":[]}\n"},
	{argv: "jq -r -a .[\"é\"] uni.json", stdout: "\"\\ud83d\\ude00\"\n"},
	{argv: "jq -j .[] two.json", stdout: "12"},
	{argv: "jq -j .", stdin: "\"a\" \"b\" 1", stdout: "ab1"},
	{argv: "jq -R .", stdin: "x\ny", stdout: "\"x\"\n\"y\"\n"},
	{argv: "jq -R . lines.txt", stdout: "\"alpha\"\n\"beta\"\n\"\"\n\"gamma\"\n"},
	{argv: "jq -Rs . lines.txt", stdout: "\"alpha\\nbeta\\n\\ngamma\"\n"},
	{argv: "jq -Rn -c [inputs]", stdin: "a\nb\n", stdout: "[\"a\",\"b\"]\n"},
	{argv: "jq -M .x", stdin: "{\"x\":1}", stdout: "1\n"},
	{argv: "jq -n -c --slurpfile v vals.json --rawfile t lines.txt [$v,$t]", stdout: "[[1,[2],{\"c\":3}],\"alpha\\nbeta\\n\\ngamma\"]\n"},
	{argv: "jq -n --slurpfile v missing.json $v", stdout: "", status: 2, stderr: "jq: Bad JSON in --slurpfile v missing.json: Could not open missing.json: No such file or directory\n"},
	{argv: "jq .a.b", stdin: "{\"a\":1}", stdout: "", status: 5, stderr: "jq: error (at <stdin>:0): Cannot index number with string \"b\"\n"},
	{argv: "jq -n error(\"x\")", stdout: "", status: 5, stderr: "jq: error (at <unknown>): x\n"},
	{argv: "jq -n error({\"a\":1})", stdout: "", status: 5, stderr: "jq: error (at <unknown>) (not a string): {\"a\":1}\n"},
	{argv: "jq .[]|error", stdin: "[1,\"m\"]", stdout: "", status: 5, stderr: "jq: error (at <stdin>:0) (not a string): 1\n"},
	{argv: "jq -c [.[]|tostring]", stdin: "[1.0,1e3,0.00001,1e17]", stdout: "[\"1.0\",\"1E+3\",\"0.00001\",\"1E+17\"]\n"},
	{argv: "jq -c [.[]*1]", stdin: "[1.0,1e3,0.00001,1e17,100000000000000000001]", stdout: "[1,1000,1e-05,1e+17,1e+20]\n"},
	{argv: "jq -c [.,input_line_number] two.json", stdout: "[{\"a\":1},1]\n[{\"a\":2},2]\n"},
	{argv: "jq input_filename two.json", stdout: "\"two.json\"\n\"two.json\"\n"},
	{argv: "jq -n [1,2]|debug|length", stdout: "2\n", stderr: "[\"DEBUG:\",[1,2]]\n"},
	{argv: "jq -c [paths]", stdin: "{\"b\":{\"d\":1},\"a\":2}", stdout: "[[\"b\"],[\"b\",\"d\"],[\"a\"]]\n"},
	{argv: "jq -c to_entries", stdin: "{\"b\":1,\"a\":2}", stdout: "[{\"key\":\"b\",\"value\":1},{\"key\":\"a\",\"value\":2}]\n"},
	{argv: "jq -c with_entries(.value+=1)", stdin: "{\"b\":1,\"a\":2}", stdout: "{\"b\":2,\"a\":3}\n"},
	{argv: "jq -c .+{\"a\":9,\"c\":3}", stdin: "{\"b\":1,\"a\":2}", stdout: "{\"b\":1,\"a\":9,\"c\":3}\n"},
}

// TestJqFormsMatchJq runs each jq form over stdin and captured files and
// compares stdout, stderr and status byte for byte with jq 1.8.1.
func TestJqFormsMatchJq(t *testing.T) {
	h := newHarness(t, queryFiles)
	for _, c := range jqCases {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec(c.stdin, splitWords(c.argv)...)
			r.expect(t, c.argv, c.stdout, c.status)
			if r.stderr != c.stderr {
				t.Fatalf("stderr %q\nwant   %q", r.stderr, c.stderr)
			}
		})
	}
}

// splitWords splits a fixture argv on spaces; queries in fixtures contain none.
func splitWords(argv string) []string { return strings.Fields(argv) }

// TestJqCompileErrorIsStatusThree: a query that does not compile is jq's
// status 3 with its diagnostic, not a refusal.
func TestJqCompileErrorIsStatusThree(t *testing.T) {
	h := newHarness(t, nil)
	r := h.exec("", "jq", ".[0")
	r.expect(t, "compile error", "", 3)
	if !strings.HasPrefix(r.stderr, "jq: error: ") || !strings.HasSuffix(r.stderr, "\njq: 1 compile error\n") {
		t.Fatalf("stderr %q", r.stderr)
	}
	imported := h.exec("", "jq", "-n", "import \"lib\" as lib; 1")
	imported.expect(t, "module loader", "", 3)
}

// TestJqEnvironmentIsTheProgramEnvironment: $ENV and env read the
// program's exported variables, never the host process.
func TestJqEnvironmentIsTheProgramEnvironment(t *testing.T) {
	h := newHarness(t, nil)
	h.env = []string{"STAGE=prod", "EMPTY="}
	h.exec("", "jq", "-n", "-c", "[$ENV.STAGE,env.EMPTY,($ENV|length)]").expect(t, "env", "[\"prod\",\"\",2]\n", 0)
	h.env = nil
	h.exec("", "jq", "-n", "-c", "$ENV").expect(t, "no env", "{}\n", 0)
}

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
	slurped := h.exec("", "jq", "-n", "--rawfile", "t", "lines.txt", "$t|length")
	if got := h.pins.pins[slurped.execution]; len(got) != 1 || got[0].Path != "lines.txt" {
		t.Fatalf("--rawfile pins %+v", got)
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
	h.exec("", "jq", "--seq", ".").refused(t, "unsupported option", "COMMAND_OPTION_UNSUPPORTED")
	h.owners.limitOverride = map[string]int64{"stream_bytes": 4}
	h.exec("", "jq", "-n", "\"long string\"").refused(t, "stream bound", "COMMAND_LIMIT_EXCEEDED")
	h.owners.limitOverride = map[string]int64{"query_work_steps": 3}
	h.exec("", "jq", "-n", "range(5)").refused(t, "work steps", "COMMAND_LIMIT_EXCEEDED")
}
