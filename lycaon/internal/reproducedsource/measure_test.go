package reproducedsource_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/reproducedsource"
)

// I3: Trailing report json fence, diff/patch fences, and caller-excluded spans never contribute.
func TestMeasureExcludedFences(t *testing.T) {
	candidates := []reproducedsource.Candidate{
		{
			DisplayPath: "config/report.json",
			Content: strings.Join([]string{
				"{",
				`  "verdict": "pass",`,
				`  "summary": "all checks green",`,
				`  "checked": true`,
				"}",
			}, "\n"),
		},
		{
			DisplayPath: "internal/app/run.go",
			Content: strings.Join([]string{
				"package app",
				"func Run() error {",
				"\treturn startEngine()",
				"}",
			}, "\n"),
		},
	}

	tests := []struct {
		name      string
		markdown  string
		excludeFn func(citationmd.Document) []citationmd.Span
		want      reproducedsource.Result
	}{
		{
			name:     "trailing_closed_report_json_fence_excluded",
			markdown: "Summary complete.\n\n```json\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"all checks green\",\n  \"checked\": true\n}\n```\n",
			want:     reproducedsource.Result{},
		},
		{
			name:     "trailing_uppercase_json_report_fence_excluded",
			markdown: "Done.\n\n```JSON\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"all checks green\",\n  \"checked\": true\n}\n```",
			want:     reproducedsource.Result{},
		},
		{
			name:     "earlier_code_fence_measured_while_trailing_report_json_excluded",
			markdown: "```go\nfunc Run() error {\n\treturn startEngine()\n}\n```\n\n```json\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"all checks green\",\n  \"checked\": true\n}\n```\n",
			want: reproducedsource.Result{
				Lines:     3,
				FileLines: 4,
				Path:      "internal/app/run.go",
				StartLine: 2,
				EndLine:   4,
			},
		},
		{
			name:     "non_trailing_json_fence_still_measured",
			markdown: "```json\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"all checks green\",\n  \"checked\": true\n}\n```\n\nClosing explanation after the block.\n",
			want: reproducedsource.Result{
				Lines:     5,
				FileLines: 5,
				Path:      "config/report.json",
				StartLine: 1,
				EndLine:   5,
			},
		},
		{
			name:     "unclosed_trailing_json_fence_is_not_report_fence",
			markdown: "```json\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"all checks green\",\n  \"checked\": true\n}",
			want: reproducedsource.Result{
				Lines:     5,
				FileLines: 5,
				Path:      "config/report.json",
				StartLine: 1,
				EndLine:   5,
			},
		},
		{
			name:     "diff_fence_quoting_source_excluded",
			markdown: "```diff\n func Run() error {\n \treturn startEngine()\n }\n```\n",
			want:     reproducedsource.Result{},
		},
		{
			name:     "diff_with_extra_info_tokens_excluded",
			markdown: "```DIFF unified\npackage app\nfunc Run() error {\n\treturn startEngine()\n}\n```\n",
			want:     reproducedsource.Result{},
		},
		{
			name:     "patch_fence_quoting_source_excluded",
			markdown: "```Patch\npackage app\nfunc Run() error {\n\treturn startEngine()\n}\n```\n",
			want:     reproducedsource.Result{},
		},
		{
			name:     "caller_excluded_span_skipped",
			markdown: "```go\nfunc Run() error {\n\treturn startEngine()\n}\n```\n",
			excludeFn: func(doc citationmd.Document) []citationmd.Span {
				return []citationmd.Span{doc.Zones[0].Span}
			},
			want: reproducedsource.Result{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseDoc(tc.markdown)
			var excluded []citationmd.Span
			if tc.excludeFn != nil {
				excluded = tc.excludeFn(doc)
			}
			got := reproducedsource.Measure(doc, excluded, candidates)
			if got != tc.want {
				t.Fatalf("Measure() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// I7: StartLine..EndLine covers matched short and blank lines; Lines counts non-blank lines; ties break in order.
func TestMeasureLineSpansNormalizationAndTieBreaking(t *testing.T) {
	bracedCandidate := reproducedsource.Candidate{
		DisplayPath: "internal/worker/loop.go",
		Content: strings.Join([]string{
			"package worker",
			"",
			"func step() {",
			"\t{",
			"",
			"\t\texecuteWorkItem()",
			"",
			"\t}",
			");",
			"end",
		}, "\n"),
	}

	duplicateWithinFile := reproducedsource.Candidate{
		DisplayPath: "internal/dup/dup.go",
		Content: strings.Join([]string{
			"package dup",
			"func firstCopy() {",
			"\trunSharedBody()",
			"\tfinishSharedBody()",
			"}",
			"func secondCopy() {",
			"\trunSharedBody()",
			"\tfinishSharedBody()",
			"}",
		}, "\n"),
	}

	tests := []struct {
		name       string
		markdown   string
		candidates []reproducedsource.Candidate
		want       reproducedsource.Result
	}{
		{
			name:       "short_lines_at_start_middle_and_end_included_with_substantive_anchor",
			markdown:   "```go\n{\n\nexecuteWorkItem()\n\n\n}\n);\nend\n```\n",
			candidates: []reproducedsource.Candidate{bracedCandidate},
			want: reproducedsource.Result{
				Lines:     5,
				FileLines: 7,
				Path:      "internal/worker/loop.go",
				StartLine: 4,
				EndLine:   10,
			},
		},
		{
			name:       "run_of_only_short_lines_rejected",
			markdown:   "```go\n}\n);\nend\n```\n",
			candidates: []reproducedsource.Candidate{bracedCandidate},
			want:       reproducedsource.Result{},
		},
		{
			name: "cat_n_prefixes_with_numbered_blank_lines_stripped",
			markdown: strings.Join([]string{
				"```",
				"     3\tfunc step() {",
				"     4\t\t{",
				"     5\t",
				"     6\t\t\texecuteWorkItem()",
				"     7\t",
				"     8\t\t}",
				"```",
			}, "\n"),
			candidates: []reproducedsource.Candidate{bracedCandidate},
			want: reproducedsource.Result{
				Lines:     4,
				FileLines: 7,
				Path:      "internal/worker/loop.go",
				StartLine: 3,
				EndLine:   8,
			},
		},
		{
			name:       "indented_code_block_measured",
			markdown:   "Observed block:\n\n    func step() {\n        {\n        executeWorkItem()\n        }\n",
			candidates: []reproducedsource.Candidate{bracedCandidate},
			want: reproducedsource.Result{
				Lines:     4,
				FileLines: 7,
				Path:      "internal/worker/loop.go",
				StartLine: 3,
				EndLine:   8,
			},
		},
		{
			name:       "blockquote_wrapped_fence_measured",
			markdown:   "> ```go\n> func step() {\n> \t{\n> \t\texecuteWorkItem()\n> ```\n",
			candidates: []reproducedsource.Candidate{bracedCandidate},
			want: reproducedsource.Result{
				Lines:     3,
				FileLines: 7,
				Path:      "internal/worker/loop.go",
				StartLine: 3,
				EndLine:   6,
			},
		},
		{
			name: "tie_breaks_by_earlier_fence",
			markdown: strings.Join([]string{
				"```go",
				"func step() {",
				"\t{",
				"\t\texecuteWorkItem()",
				"```",
				"",
				"```go",
				"runSharedBody()",
				"finishSharedBody()",
				"}",
				"```",
			}, "\n"),
			candidates: []reproducedsource.Candidate{duplicateWithinFile, bracedCandidate},
			want: reproducedsource.Result{
				Lines:     3,
				FileLines: 7,
				Path:      "internal/worker/loop.go",
				StartLine: 3,
				EndLine:   6,
			},
		},
		{
			name:     "tie_breaks_by_earlier_candidate_then_earlier_line",
			markdown: "```go\nrunSharedBody()\nfinishSharedBody()\n}\n```\n",
			candidates: []reproducedsource.Candidate{
				duplicateWithinFile,
				{
					DisplayPath: "internal/dup/other.go",
					Content:     duplicateWithinFile.Content,
				},
			},
			want: reproducedsource.Result{
				Lines:     3,
				FileLines: 9,
				Path:      "internal/dup/dup.go",
				StartLine: 3,
				EndLine:   5,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reproducedsource.Measure(parseDoc(tc.markdown), nil, tc.candidates)
			if got != tc.want {
				t.Fatalf("Measure() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
