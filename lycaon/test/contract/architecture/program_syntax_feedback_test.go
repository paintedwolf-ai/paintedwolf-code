package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcommand"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProgramSyntaxFeedbackUsesTypedSpansWithoutGuessedRepairs(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load program syntax feedback", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	for _, sourced := range []bool{false, true} {
		for _, source := range []string{"printf ok; if", "if false; then trap unknown; fi", "printf ok; echo ${(q)name}", strings.Repeat(" ", int(commandsurface.SyntaxUnitByteLimit(sourced))+1)} {
			_, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "fixture.pw", Sourced: sourced})
			rejection := toolcommand.ProgramSyntaxObservation("command", err)
			if rejection == nil {
				t.Fatalf("missing typed program feedback (sourced=%t): %v", sourced, err)
			}
			out, err := formatter.Format(rejection.Code, rejection.Data)
			contractcheck.FailErr(t, "render syntax feedback", err)
			wantFacts := []string{"Code: " + rejection.Code}
			if !sourced {
				wantFacts = append(wantFacts, "State: No command operation ran or changed files.", "The submitted source was refused before evaluation")
				if rejection.Code == "COMMAND_LIMIT_EXCEEDED" {
					if rejection.Data["limit_id"] != "program_bytes" || rejection.Data["resource_unit"] != "bytes" {
						t.Fatalf("resource feedback lacks typed limit facts: %+v", rejection.Data)
					}
				} else {
					wantFacts = append(wantFacts, "1:")
				}
			} else {
				switch rejection.Code {
				case "COMMAND_LIMIT_EXCEEDED":
					if rejection.Data["limit_id"] != "source_bytes" || rejection.Data["resource_unit"] != "bytes" {
						t.Fatalf("sourced resource feedback lacks typed limit facts: %+v", rejection.Data)
					}
					wantFacts = append(wantFacts, "The command stopped at the recorded boundary")
				case "COMMAND_SYNTAX_UNSUPPORTED":
					wantFacts = append(wantFacts, "1:", "Earlier commands that finished still count")
				default:
					wantFacts = append(wantFacts, "1:", "Earlier parent operations may have run")
				}
			}
			for _, want := range wantFacts {
				if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
					t.Fatalf("feedback (sourced=%t) missing %q: %s", sourced, want, out)
				}
			}
			if strings.Contains(out, "replacement_args") || strings.Contains(out, "sh -c") {
				t.Fatalf("invented repair or fallback: %s", out)
			}
		}
	}
}
