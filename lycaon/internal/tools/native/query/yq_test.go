package query

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

var yqFiles = map[string]string{
	"doc.json":   `{"b":{},"a":[1,2],"name":"svc"}` + "\n",
	"cfg.yaml":   "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\nname: api\n",
	"multi.yaml": "kind: A\n---\nkind: B\n",
	"cfg.toml":   "[package]\nname = \"demo\"\nedition = 2021\n",
	"two.yaml":   "a: 1\n",
	"three.yaml": "a: 2\nb: [x]\n",
	"data.csv":   "name,age\nann,3\nbob,4\n",
}

// yqCases are mikefarah yq v4.54.1's own stdout, stderr and status for each
// form, recorded from the yq binary built from the pinned module (with its
// environment and file operators disabled, as the host runs them).
var yqCases = []queryCase{
	{argv: "yq .server.port cfg.yaml", stdout: "8080\n"},
	{argv: "yq .server cfg.yaml", stdout: "port: 8080 # the port\nhosts: [a, b]\n"},
	{argv: "yq cfg.yaml", stdout: "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\nname: api\n"},
	{argv: "yq . cfg.yaml", stdout: "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\nname: api\n"},
	{argv: "yq -o json .server cfg.yaml", stdout: "{\n  \"port\": 8080,\n  \"hosts\": [\n    \"a\",\n    \"b\"\n  ]\n}\n"},
	{argv: "yq -o=json .server cfg.yaml", stdout: "{\n  \"port\": 8080,\n  \"hosts\": [\n    \"a\",\n    \"b\"\n  ]\n}\n"},
	{argv: "yq -ojson -I0 . cfg.yaml", stdout: "{\"server\":{\"port\":8080,\"hosts\":[\"a\",\"b\"]},\"name\":\"api\"}\n"},
	{argv: "yq --output-format=json .name cfg.yaml", stdout: "\"api\"\n"},
	{argv: "yq -j .server.hosts cfg.yaml", stdout: "[\n  \"a\",\n  \"b\"\n]\n", stderr: "Flag --tojson has been deprecated, please use -o=json instead\n"},
	{argv: "yq -r .name cfg.yaml", stdout: "api\n"},
	{argv: "yq -o json -r .name cfg.yaml", stdout: "api\n"},
	{argv: "yq -o json .name cfg.yaml", stdout: "\"api\"\n"},
	{argv: "yq -I4 . cfg.yaml", stdout: "# settings\nserver:\n    port: 8080 # the port\n    hosts: [a, b]\nname: api\n"},
	{argv: "yq -P . cfg.yaml", stdout: "# settings\nserver:\n  port: 8080 # the port\n  hosts:\n    - a\n    - b\nname: api\n"},
	{argv: "yq -P -o json . cfg.yaml", stdout: "{\n  \"server\": {\n    \"port\": 8080,\n    \"hosts\": [\n      \"a\",\n      \"b\"\n    ]\n  },\n  \"name\": \"api\"\n}\n"},
	{argv: "yq .kind multi.yaml", stdout: "A\n---\nB\n"},
	{argv: "yq . multi.yaml", stdout: "kind: A\n---\nkind: B\n"},
	{argv: "yq -N .kind multi.yaml", stdout: "A\nB\n"},
	{argv: "yq ea [.kind] multi.yaml", stdout: "- A\n- B\n"},
	{argv: "yq eval-all .a two.yaml three.yaml", stdout: "1\n---\n2\n"},
	{argv: "yq ea select(fi==0)*select(fi==1) two.yaml three.yaml", stdout: "a: 2\nb: [x]\n"},
	{argv: "yq ea . two.yaml three.yaml", stdout: "a: 1\n---\na: 2\nb: [x]\n"},
	{argv: "yq e .a two.yaml three.yaml", stdout: "1\n---\n2\n"},
	{argv: "yq eval .a three.yaml", stdout: "2\n"},
	{argv: "yq .package.edition cfg.toml", stdout: "2021\n"},
	{argv: "yq -oy . cfg.toml", stdout: "package:\n  name: demo\n  edition: 2021\n"},
	{argv: "yq . doc.json", stdout: "{\n  \"b\": {},\n  \"a\": [\n    1,\n    2\n  ],\n  \"name\": \"svc\"\n}\n"},
	{argv: "yq -p json -o yaml . doc.json", stdout: "b: {}\na:\n  - 1\n  - 2\nname: svc\n"},
	{argv: "yq -o yaml . doc.json", stdout: "b: {}\na:\n  - 1\n  - 2\nname: svc\n"},
	{argv: "yq -o props . cfg.yaml", stdout: "# settings\n# the port\nserver.port = 8080\nserver.hosts.0 = a\nserver.hosts.1 = b\nname = api\n"},
	{argv: "yq -p csv -o json . data.csv", stdout: "[\n  {\n    \"name\": \"ann\",\n    \"age\": 3\n  },\n  {\n    \"name\": \"bob\",\n    \"age\": 4\n  }\n]\n"},
	{argv: "yq -o csv .server.hosts cfg.yaml", stdout: "a,b\n"},
	{argv: "yq .x", stdin: "x: hello\n", stdout: "hello\n"},
	{argv: "yq", stdin: "x: 1\n", stdout: "x: 1\n"},
	{argv: "yq -", stdin: "x: 1\n", stdout: "x: 1\n"},
	{argv: "yq .x -", stdin: "x: 1\n", stdout: "1\n"},
	{argv: "yq -r=false .x", stdin: "x: \"hi\"\n", stdout: "\"hi\"\n"},
	{argv: "yq --unwrapScalar=false .x", stdin: "x: \"hi\"\n", stdout: "\"hi\"\n"},
	{argv: "yq -e .missing", stdin: "a: 1\n", stdout: "null\n", status: 1, stderr: "Error: no matches found\n"},
	{argv: "yq -e .a", stdin: "a: 1\n", stdout: "1\n"},
	{argv: "yq -e .a", stdin: "a: false\n", stdout: "false\n", status: 1, stderr: "Error: no matches found\n"},
	{argv: "yq .a nope.yaml", stdout: "", status: 1, stderr: "Error: open nope.yaml: no such file or directory\n"},
	{argv: "yq .a[ cfg.yaml", stdout: "", status: 1, stderr: "Error: bad expression, could not find matching `]`\n"},
	{argv: "yq .", stdin: "a: [\n", stdout: "", status: 1, stderr: "Error: bad file '-': go-yaml load error in parser (while parsing a flow node) at L2.C1: did not find expected node content\n"},
	{argv: "yq -n .a.b=1", stdout: "a:\n  b: 1\n"},
	{argv: "yq -n .a=env(HOME)", stdout: "", status: 1, stderr: "Error: env operations have been disabled\n"},
	{argv: "yq -n load(\"cfg.yaml\")", stdout: "", status: 1, stderr: "Error: file operations have been disabled\n"},
	{argv: "yq -n .x cfg.yaml", stdout: "", status: 1, stderr: "Error: cannot pass files in when using null-input flag\n"},
	{argv: "yq .server.port=9090|.name=\"web\" cfg.yaml", stdout: "# settings\nserver:\n  port: 9090 # the port\n  hosts: [a, b]\nname: web\n"},
	{argv: "yq .server.hosts+=[\"c\"] cfg.yaml", stdout: "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\n  hosts+:\n    - c\nname: api\n"},
	{argv: "yq del(.server) cfg.yaml", stdout: "# settings\nname: api\n"},
	{argv: "yq .server|keys cfg.yaml", stdout: "- port\n- hosts\n"},
	{argv: "yq .server|to_entries cfg.yaml", stdout: "- key: port\n  value: 8080 # the port\n- key: hosts\n  value: [a, b]\n"},
	{argv: "yq .server.hosts|length cfg.yaml", stdout: "2\n"},
	{argv: "yq [.server.hosts[]|upcase] cfg.yaml", stdout: "- A\n- B\n"},
	{argv: "yq .server.hosts[]|select(.==\"b\") cfg.yaml", stdout: "b\n"},
	{argv: "yq --expression .name cfg.yaml", stdout: "api\n"},
	{argv: "yq -0 .server.hosts[] cfg.yaml", stdout: "a\u0000b\u0000"},
	{argv: "yq .a two.yaml three.yaml", stdout: "1\n---\n2\n"},
	{argv: "yq -o json -I0 . two.yaml three.yaml", stdout: "{\"a\":1}\n{\"a\":2,\"b\":[\"x\"]}\n"},
	{argv: "yq . cfg.yaml two.yaml", stdout: "# settings\nserver:\n  port: 8080 # the port\n  hosts: [a, b]\nname: api\n---\na: 1\n"},
	{argv: "yq -i", stdout: "", status: 1, stderr: "Error: write in place flag only applicable when giving an expression and at least one file\n"},
	{argv: "yq -i .a=1", stdin: "x: 1\n", stdout: "", status: 1, stderr: "Error: write in place flag only applicable when giving an expression and at least one file\n"},
	{argv: "yq filename cfg.yaml", stdout: "cfg.yaml\n"},
	{argv: "yq ea [filename] two.yaml three.yaml", stdout: "- two.yaml\n- three.yaml\n"},
	{argv: "yq .a|@json", stdin: "a: {b: 1}\n", stdout: "{\"b\":1}\n"},
	{argv: "yq -o=xml .", stdin: "a: {b: 1}\n", stdout: "<a>\n  <b>1</b>\n</a>\n"},
	{argv: "yq -p=xml -o=json .", stdin: "<a><b>1</b></a>\n", stdout: "{\n  \"a\": {\n    \"b\": \"1\"\n  }\n}\n"},
}

// TestYqFormsMatchYq compares every yq form with the yq binary's bytes.
func TestYqFormsMatchYq(t *testing.T) {
	h := newHarness(t, yqFiles)
	for _, c := range yqCases {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec(c.stdin, splitWords(c.argv)...)
			r.expect(t, c.argv, c.stdout, c.status)
			if r.stderr != c.stderr {
				t.Fatalf("stderr %q\nwant   %q", r.stderr, c.stderr)
			}
		})
	}
}

// TestYqInPlacePublishesAtCapturedRevisions: -i writes the first file's
// result through the write door, keeping comments and layout, and pins the
// source and destination it read.
func TestYqInPlacePublishesAtCapturedRevisions(t *testing.T) {
	h := newHarness(t, yqFiles)
	r := h.exec("", "yq", "-i", ".server.port=9090|.name=\"web\"", "cfg.yaml")
	r.expect(t, "yq -i", "", 0)
	if !r.result.PublicationEligible || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("result %+v", r.result)
	}
	if got, want := h.disk("cfg.yaml"), "# settings\nserver:\n  port: 9090 # the port\n  hosts: [a, b]\nname: web\n"; got != want {
		t.Fatalf("disk %q\nwant %q", got, want)
	}
	pins := h.pins.pins[r.execution]
	if len(pins) != 2 || pins[0].Role != toolcontract.SourceRevision || pins[1].Role != toolcontract.DestinationRevision {
		t.Fatalf("pins %+v", pins)
	}
	unchanged := h.exec("", "yq", "-i", ".", "two.yaml")
	unchanged.expect(t, "unchanged -i", "", 0)
	if unchanged.result.EffectCertainty != toolcontract.NoEffect || h.disk("two.yaml") != yqFiles["two.yaml"] {
		t.Fatalf("unchanged result %+v", unchanged.result)
	}
	missing := h.exec("", "yq", "-i", ".a=1", "nope.yaml")
	missing.expect(t, "missing -i", "", 1)
}

// pythonYqCases are python yq spellings: jq programs over YAML documents,
// jq's JSON output unless -y, and jq's statuses. Outputs are the jq 1.8.1
// result over the same documents as JSON, with PyYAML's scalar end marker
// for -y; runtime error positions name the YAML file.
var pythonYqCases = []queryCase{
	{argv: "yq -c .server cfg.yaml", stdout: "{\"port\":8080,\"hosts\":[\"a\",\"b\"]}\n"},
	{argv: "yq -c keys_unsorted cfg.yaml", stdout: "[\"server\",\"name\"]\n"},
	{argv: "yq -y .server cfg.yaml", stdout: "port: 8080\nhosts:\n  - a\n  - b\n"},
	{argv: "yq -y .name cfg.yaml", stdout: "api\n...\n"},
	{argv: "yq --yaml-output .server.hosts[] cfg.yaml", stdout: "a\n...\n---\nb\n...\n"},
	{argv: "yq -s -c [.[].kind] multi.yaml", stdout: "[\"A\",\"B\"]\n"},
	{argv: "yq -c --arg x v {x:$x,n:.name} cfg.yaml", stdout: "{\"x\":\"v\",\"n\":\"api\"}\n"},
	{argv: "yq -S -c . three.yaml", stdout: "{\"a\":2,\"b\":[\"x\"]}\n"},
	{argv: "yq -c .a.b three.yaml", stdout: "", stderr: "jq: error (at three.yaml:2): Cannot index number with string \"b\"\n", status: 5},
	{argv: "yq -c .a", stdin: "a: [1, 2]\n", stdout: "[1,2]\n"},
}

// TestPythonYqSpellingsRunJqOverYAML: a python yq flag selects that dialect.
func TestPythonYqSpellingsRunJqOverYAML(t *testing.T) {
	h := newHarness(t, yqFiles)
	for _, c := range pythonYqCases {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec(c.stdin, splitWords(c.argv)...)
			r.expect(t, c.argv, c.stdout, c.status)
			if r.stderr != c.stderr {
				t.Fatalf("stderr %q\nwant   %q", r.stderr, c.stderr)
			}
		})
	}
	r := h.exec("", "yq", "-i", "-y", ".name = \"web\"", "cfg.yaml")
	r.expect(t, "python yq -i", "", 0)
	if got, want := h.disk("cfg.yaml"), "server:\n  port: 8080\n  hosts:\n    - a\n    - b\nname: web\n"; got != want || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("disk %q want %q result %+v", got, want, r.result)
	}
}

// TestYqRefusesOutsideTheProject: a file outside the confinement is a typed
// refusal, never an ordinary yq error.
func TestYqRefusesOutsideTheProject(t *testing.T) {
	h := newHarness(t, yqFiles)
	outside := h.exec("", "yq", ".", "/etc/hosts")
	if toolrejection.AsToolReject(outside.err) == nil || outside.result.Terminal != toolcontract.ProducerRefused || outside.stdout != "" || len(h.pins.pins[outside.execution]) != 0 {
		t.Fatalf("outside read: %+v err %v stdout %q", outside.result, outside.err, outside.stdout)
	}
	h.exec("", "yq", "-o", "json", "--split-exp", ".a", "two.yaml").refused(t, "split files", "COMMAND_OPTION_UNSUPPORTED")
}
