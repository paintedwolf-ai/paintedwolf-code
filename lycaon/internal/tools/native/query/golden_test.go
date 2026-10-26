package query

import (
	"encoding/json"
	"errors"
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

// versionSpecific are jqCases whose output jq 1.7 prints differently
// (--indent 0 layout, usage diagnostics naming argv[0]).
var versionSpecific = map[string]bool{
	"jq --indent 0 . doc.json":            true,
	"jq -n --slurpfile v missing.json $v": true,
}

func hostJqBinary() string {
	for _, c := range []string{"/opt/homebrew/bin/jq", "/usr/local/bin/jq"} {
		if out, err := exec.Command(c, "--version").Output(); err == nil && strings.HasPrefix(string(out), "jq-1.8") {
			return c
		}
	}
	binary, _ := exec.LookPath("jq")
	return binary
}

// TestDifferentialAgainstHostJq runs every jqCase that jq 1.7 and 1.8 agree
// on through an installed jq as well, so the recorded goldens are checked
// against the real binary wherever one is available. Diagnostic wording
// changed between releases, so stderr is compared only against jq 1.8.
func TestDifferentialAgainstHostJq(t *testing.T) {
	binary := hostJqBinary()
	if binary == "" {
		t.Log("no host jq; the recorded jq 1.8.1 goldens still apply")
		return
	}
	version, err := exec.Command(binary, "--version").Output()
	testutil.FailErr(t, "host jq version", err)
	sameRelease := strings.HasPrefix(string(version), "jq-1.8")
	h := newHarness(t, queryFiles)
	compared := 0
	for _, c := range jqCases {
		if versionSpecific[c.argv] {
			continue
		}
		t.Run(c.argv, func(t *testing.T) {
			words := splitWords(c.argv)
			cmd := exec.Command(binary, words[1:]...)
			cmd.Dir = h.root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			cmd.Stdin = strings.NewReader(c.stdin)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			status := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			r := h.exec(c.stdin, words...)
			r.expect(t, c.argv, string(out), status)
			if sameRelease && r.stderr != stderr.String() {
				t.Fatalf("stderr %q\nhost   %q", r.stderr, stderr.String())
			}
		})
		compared++
	}
	if compared < 60 {
		t.Fatalf("compared %d forms", compared)
	}
}
