package contract

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These packages emit JSON that can reach model context.
var surveyjsonRequiredPackages = []string{
	"lycaon/internal/tools/native",
	"lycaon/internal/scan",
	"lycaon/internal/repomap",
	"lycaon/internal/git",
	"lycaon/internal/webresearch",
	"lycaon/internal/coordinator/loopwake",
	"lycaon/internal/tooloutput",
	"lycaon/internal/browser",
}

// These files emit JSON outside model context.
var surveyJSONMarshalExemptFile = map[string]string{
	"lycaon/internal/scan/output/ledger_export.go":     "HTTP SARIF/OpenVEX attachment exports; not model tool output",
	"lycaon/internal/scan/bundled/build_identity.go":   "packaged build identity hashing",
	"lycaon/internal/scan/bundled/release_audit.go":    "offline release audit artifact",
	"lycaon/internal/scan/bundled/release_evidence.go": "authenticated release evidence artifact",
	"lycaon/internal/scan/dedup.go":                    "internal dedup hash; payload never reaches LLM",
	"lycaon/internal/scan/store_results.go":            "SQL persistence; payload never reaches LLM directly",
	"lycaon/internal/scan/store_execution.go":          "SQL persistence of runtime policy and progress; payload never reaches LLM directly",
	"lycaon/internal/scan/result_spill.go":             "oversized result_json spill file + SQLite stub; never LLM wire",
	"lycaon/internal/webresearch/serper.go":            "provider HTTP request body; not LLM tool output",
	"lycaon/internal/webresearch/tavily.go":            "provider HTTP request body; not LLM tool output",
	"lycaon/internal/webresearch/microsoft_learn.go":   "provider HTTP request body; not LLM tool output",
	"lycaon/internal/browser/filmstrip.go":             "zip manifest for Den asset pack; not tool_result JSON",
}

var stdlibJSONMarshalRe = regexp.MustCompile(`\bjson\.(Marshal|MarshalIndent|NewEncoder)\b`)

func TestSurveyToolsUseSurveyJSONMarshal(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var hits []string
	for _, relPkg := range surveyjsonRequiredPackages {
		dir := filepath.Join(root, relPkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if _, exempt := surveyJSONMarshalExemptFile[rel]; exempt {
				return nil
			}
			text := string(body)
			// The wrapper configures the standard-library encoder.
			if strings.Contains(rel, "lycaon/internal/tools/surveyjson") {
				return nil
			}
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				// Skip comments — examples and docstrings may mention the API by name.
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
					continue
				}
				if stdlibJSONMarshalRe.MatchString(line) {
					hits = append(hits, fmt.Sprintf("%s:%d  %s", rel, i+1, strings.TrimSpace(line)))
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "walk "+relPkg, err)
	}
	sort.Strings(hits)
	if len(hits) > 0 {
		t.Fatalf(`survey-emitting tool packages use stdlib encoding/json — switch to
github.com/lycaon/lycaon/internal/tools/surveyjson.Marshal so HTML-unsafe
characters stay literal in the JSON the model reads.

Add the file path to surveyJSONMarshalExemptFile only if the payload truly
never reaches the LLM (server-internal use, structured-error rendering).

Hits:
  %s`, strings.Join(hits, "\n  "))
	}
}

// TestSurveyJSONMarshalKeepsHTMLLiteral covers exact source text in model context.
func TestSurveyJSONMarshalKeepsHTMLLiteral(t *testing.T) {
	t.Parallel()
	type response struct {
		Content string `json:"content"`
	}
	raw, err := surveyjson.Marshal(response{Content: "<div> & </div>"})
	if err != nil {
		t.Fatalf("surveyjson.Marshal: %v", err)
	}
	out := string(raw)
	for _, want := range []string{"<div>", "</div>", "& "} {
		if !strings.Contains(out, want) {
			t.Fatalf("survey marshal lost literal %q in %q", want, out)
		}
	}
}

// TestSurveyJSONMarshalNoTrailingNewline protects concatenated envelopes.
func TestSurveyJSONMarshalNoTrailingNewline(t *testing.T) {
	t.Parallel()
	raw, err := surveyjson.Marshal(map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("surveyjson.Marshal: %v", err)
	}
	if len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == ' ') {
		t.Fatalf("surveyjson.Marshal output must be trimmed of trailing whitespace, got %q", string(raw))
	}
}
