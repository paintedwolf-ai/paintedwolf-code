package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

func TestBB10CommandDoorInvariants(t *testing.T) {
	t.Parallel()

	launchRule := rule(t, forbiddencalls.Family, "command-external-starts-through-launch-door")
	launchClean := map[string]string{
		"lycaon/internal/exec/pipeline.go": "package exec\n\nfunc StartPipelineAsync() {}\n",
		"lycaon/internal/tools/native/command/program_external.go": "package command\n\n" +
			"import lycexec \"github.com/lycaon/lycaon/internal/exec\"\n\n" +
			"type ExternalStages struct{}\n\n" +
			"func (ExternalStages) Launch() { lycexec.StartPipelineAsync() }\n",
	}

	readRule := rule(t, forbiddencalls.Family, "command-read-credit-through-delivery-bridge")
	readClean := map[string]string{
		"lycaon/internal/tools/editor_documents.go": "package tools\n\ntype EditorDocuments interface {\n\tRememberAgentRead()\n\tRememberAgentReadSpans()\n}\n",
		"lycaon/internal/tools/command_source_delivery.go": "package tools\n\nfunc RememberCommandSourceDelivery(d EditorDocuments) { d.RememberAgentRead() }\n",
		"lycaon/internal/commandprogram/eval.go":           "package commandprogram\n\nfunc Eval() {}\n",
		"lycaon/internal/tools/native/command/delivery.go": "package command\n\nimport \"github.com/lycaon/lycaon/internal/tools\"\n\n" +
			"func deliver(d tools.EditorDocuments) { tools.RememberCommandSourceDelivery(d) }\n",
	}

	cases := []struct {
		name      string
		rule      invariants.Invariant
		files     map[string]string
		want      outcome
		wantCount int
	}{
		{
			name:  "i11_clean",
			rule:  launchRule,
			files: launchClean,
			want:  passes,
		},
		{
			name: "i11_seeded_second_dispatch_bypass",
			rule: launchRule,
			files: with(launchClean, map[string]string{
				"lycaon/internal/tools/native/command/bypass.go": "package command\n\n" +
					"import lycexec \"github.com/lycaon/lycaon/internal/exec\"\n\n" +
					"func secondDispatch() { lycexec.StartPipelineAsync() }\n",
			}),
			want:      violates,
			wantCount: 1,
		},
		{
			name: "i11_renamed_anchor_fails_closed",
			rule: launchRule,
			files: with(launchClean, map[string]string{
				"lycaon/internal/tools/native/command/program_external.go": "package command\n\n" +
					"type ExternalStages struct{}\n\n" +
					"func (ExternalStages) Start() {}\n",
			}),
			want: missesAnchor,
		},
		{
			name:  "i11_empty_corpus_fails_closed",
			rule:  launchRule,
			files: nil,
			want:  fails,
		},
		{
			name:  "i13_clean",
			rule:  readRule,
			files: readClean,
			want:  passes,
		},
		{
			name: "i13_seeded_direct_read_credit_in_command_runtime",
			rule: readRule,
			files: with(readClean, map[string]string{
				"lycaon/internal/tools/native/command/delivery.go": "package command\n\nimport \"github.com/lycaon/lycaon/internal/tools\"\n\n" +
					"func deliver(d tools.EditorDocuments) { d.RememberAgentRead() }\n",
			}),
			want:      violates,
			wantCount: 1,
		},
		{
			name: "i13_seeded_direct_read_spans_in_evaluator",
			rule: readRule,
			files: with(readClean, map[string]string{
				"lycaon/internal/commandprogram/eval.go": "package commandprogram\n\nimport \"github.com/lycaon/lycaon/internal/tools\"\n\n" +
					"func Eval(d tools.EditorDocuments) { d.RememberAgentReadSpans() }\n",
			}),
			want:      violates,
			wantCount: 1,
		},
		{
			name: "i13_renamed_anchor_fails_closed",
			rule: readRule,
			files: with(readClean, map[string]string{
				"lycaon/internal/tools/command_source_delivery.go": "package tools\n\nfunc RememberDelivery() {}\n",
			}),
			want: missesAnchor,
		},
		{
			name:  "i13_empty_corpus_fails_closed",
			rule:  readRule,
			files: nil,
			want:  fails,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, tc.wantCount)
		})
	}
}
