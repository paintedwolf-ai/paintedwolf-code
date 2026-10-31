package literal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// catalogSetup is the shared fixture corpus the catalog examples declare.
type catalogExample struct {
	Program string                     `json:"program"`
	Stdout  string                     `json:"stdout"`
	Status  int                        `json:"status"`
	Setup   map[string]json.RawMessage `json:"setup"`
}

type goldenProgram struct {
	ID      string `json:"id"`
	Program string `json:"program"`
	Setup   struct {
		Files map[string]json.RawMessage `json:"files"`
	} `json:"setup"`
	Expected struct {
		Stdout     string  `json:"stdout"`
		Stderr     string  `json:"stderr"`
		Status     int     `json:"status"`
		Diagnostic *string `json:"diagnostic"`
	} `json:"expected"`
}

// splitProgram reads the literal fixtures' one form: an optional
// "printf 'BYTES' |" producer and one simple command of plain or
// single-quoted words. It is a fixture reader, not a shell.
func splitProgram(t *testing.T, program string) (stdin string, argv []string, ok bool) {
	t.Helper()
	if strings.ContainsAny(program, ";&<>$`()\n") {
		return "", nil, false
	}
	stages := strings.Split(program, " | ")
	if len(stages) > 2 {
		return "", nil, false
	}
	if len(stages) == 2 {
		words := words(stages[0])
		if len(words) != 2 || words[0] != "printf" {
			return "", nil, false
		}
		stdin = printfBytes(words[1])
		stages = stages[1:]
	}
	return stdin, words(stages[0]), true
}

func words(stage string) []string {
	var out []string
	var current strings.Builder
	quoted, any := false, false
	for i := 0; i < len(stage); i++ {
		c := stage[i]
		switch {
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

func printfBytes(format string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\0`, "\x00", `\\`, `\`)
	return replacer.Replace(format)
}

// TestCatalogGoldenProgramsProduceExactBytes runs every golden program whose
// only effects are this package's utilities, against real fixture files.
func TestCatalogGoldenProgramsProduceExactBytes(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []goldenProgram `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden contract", json.Unmarshal(catalog.Contracts["golden"], &contract))
	owners := newHarness(t, nil).owners
	ran := map[string]bool{}
	for _, golden := range contract.Golden {
		stdin, argv, ok := splitProgram(t, golden.Program)
		if !ok || len(argv) == 0 {
			continue
		}
		if _, owned := owners.Operation(argv[0]); !owned {
			continue
		}
		t.Run(golden.ID, func(t *testing.T) {
			h := newHarness(t, nil)
			for name, raw := range golden.Setup.Files {
				var content string
				testutil.FailErr(t, "golden file "+name, json.Unmarshal(raw, &content))
				seed(t, h.root, map[string]string{name: content})
			}
			r := h.exec(stdin, argv...)
			if golden.Expected.Diagnostic != nil {
				r.refused(t, golden.ID, *golden.Expected.Diagnostic)
				if r.result.Status != golden.Expected.Status || r.stdout != golden.Expected.Stdout {
					t.Fatalf("refusal status %d stdout %q", r.result.Status, r.stdout)
				}
				return
			}
			r.expect(t, golden.ID, golden.Expected.Stdout, golden.Expected.Stderr, golden.Expected.Status)
		})
		ran[golden.ID] = true
	}
	for _, id := range []string{"lf-byte-count", "stat-gnu-size", "stat-bsd-size", "base64-gnu", "base64-bsd", "binary-stream", "early-consumer", "unsupported-option",
		"du-apparent-total", "cmp-silent-different", "cmp-list-offsets", "od-hex-bytes", "strings-min-length"} {
		if !ran[id] {
			t.Errorf("golden program %s was not exercised", id)
		}
	}
}

// TestCatalogCommandExamplesProduceExactBytes runs each literal command's
// documented example in the shared fixture corpus it declares.
func TestCatalogCommandExamplesProduceExactBytes(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	owners := newHarness(t, nil).owners
	for _, spec := range catalog.Commands {
		if _, owned := owners.Operation(spec.Name); !owned {
			continue
		}
		switch spec.Name {
		case "outline", "summarize", "repo-map":
			continue // structured domain output; covered by domain fixtures
		}
		var example catalogExample
		testutil.FailErr(t, "decode example", json.Unmarshal(spec.Example, &example))
		t.Run(spec.Name, func(t *testing.T) {
			h := newHarness(t, nil)
			for name, raw := range example.Setup {
				var content string
				if json.Unmarshal(raw, &content) == nil {
					seed(t, h.root, map[string]string{name: content})
					continue
				}
				var entry struct {
					Symlink   string `json:"symlink"`
					Directory bool   `json:"directory"`
				}
				testutil.FailErr(t, "decode setup "+name, json.Unmarshal(raw, &entry))
				switch {
				case entry.Directory:
					seed(t, h.root, map[string]string{name + "/": ""})
				case entry.Symlink != "":
					testutil.FailErr(t, "link "+name, os.Symlink(entry.Symlink, filepath.Join(h.root, name)))
				}
			}
			stdin, argv, ok := splitProgram(t, example.Program)
			if !ok {
				t.Fatalf("example %q is not a literal fixture", example.Program)
			}
			h.exec(stdin, argv...).expect(t, example.Program, example.Stdout, "", example.Status)
		})
	}
}
