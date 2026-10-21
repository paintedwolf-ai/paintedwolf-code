package query

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestCatalogQueryExamples runs each owned command's documented example in
// the fixture corpus it declares.
func TestCatalogQueryExamples(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	owners := newHarness(t, nil).owners
	ran := 0
	for _, spec := range catalog.Commands {
		if _, ok := owners.Operation(spec.Name); !ok {
			continue
		}
		var example struct {
			Program string                     `json:"program"`
			Stdout  string                     `json:"stdout"`
			Status  int                        `json:"status"`
			Setup   map[string]json.RawMessage `json:"setup"`
		}
		testutil.FailErr(t, "decode example", json.Unmarshal(spec.Example, &example))
		files := map[string]string{}
		for name, raw := range example.Setup {
			var content string
			if json.Unmarshal(raw, &content) == nil {
				files[name] = content
			}
		}
		t.Run(spec.Name, func(t *testing.T) {
			h := newHarness(t, files)
			stdin, argv := exampleArgv(t, example.Program)
			h.exec(stdin, argv...).expect(t, example.Program, example.Stdout, example.Status)
		})
		ran++
	}
	if ran != 3 {
		t.Fatalf("examples run = %d", ran)
	}
}

// exampleArgv reads the examples' one shape: an optional printf producer
// and one command of plain or single-quoted words.
func exampleArgv(t *testing.T, program string) (string, []string) {
	t.Helper()
	stdin := ""
	if producer, rest, ok := strings.Cut(program, " | "); ok {
		words := shellWords(producer)
		if len(words) != 2 || words[0] != "printf" {
			t.Fatalf("unexpected producer %q", producer)
		}
		stdin = strings.ReplaceAll(words[1], `\n`, "\n")
		program = rest
	}
	return stdin, shellWords(program)
}

func shellWords(s string) []string {
	var out []string
	var current strings.Builder
	quoted, any := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted, any = !quoted, true
		case c == ' ' && !quoted:
			if any {
				out = append(out, current.String())
			}
			current.Reset()
			any = false
		default:
			current.WriteByte(c)
			any = true
		}
	}
	if any {
		out = append(out, current.String())
	}
	return out
}

// differentialCases are forms where jq's own output is gojq's: sorted keys,
// integers and strings, no error statuses (which jq versions report
// differently than the catalog's status 2).
var differentialCases = []queryCase{
	{argv: "jq .a", stdin: "{\"a\":1}\n{\"a\":2}"},
	{argv: "jq . sorted.json"},
	{argv: "jq -c . sorted.json"},
	{argv: "jq -r .name sorted.json"},
	{argv: "jq -r .[]", stdin: `["x","y"]`},
	{argv: "jq -s -c .", stdin: "1 2 3"},
	{argv: "jq -n -c [inputs]", stdin: "1 2"},
	{argv: "jq -c [.,input]", stdin: "1 2 3 4"},
	{argv: "jq -n -c --arg x v --argjson y {\"z\":1} [$x,$y]"},
	{argv: "jq -e .", stdin: "false"},
	{argv: "jq -e .", stdin: "null"},
	{argv: "jq -e .[]", stdin: "[]"},
	{argv: "jq -c .[]|.*2", stdin: "[0.1,1.25,7]"},
	{argv: "jq -c keys", stdin: `{"b":1,"a":2}`},
}

// TestDifferentialAgainstHostJq compares with an installed jq where one
// exists. gojq is the compatibility target; these forms are the ones where
// jq agrees with it.
func TestDifferentialAgainstHostJq(t *testing.T) {
	binary, err := exec.LookPath("jq")
	if err != nil {
		t.Log("no host jq to compare")
		return
	}
	h := newHarness(t, queryFiles)
	for _, c := range differentialCases {
		t.Run(c.argv, func(t *testing.T) {
			words := splitWords(c.argv)
			cmd := exec.Command(binary, words[1:]...)
			cmd.Dir = h.root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			cmd.Stdin = strings.NewReader(c.stdin)
			out, err := cmd.Output()
			status := 0
			if exit, ok := err.(*exec.ExitError); ok {
				status = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			h.exec(c.stdin, words...).expect(t, c.argv, string(out), status)
		})
	}
}
