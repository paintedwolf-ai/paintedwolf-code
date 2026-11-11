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
			if !strings.Contains(out, "Code: "+rejection.Code) {
				t.Fatalf("feedback (sourced=%t) lost its code: %s", sourced, out)
			}
			if rejection.Code == "COMMAND_LIMIT_EXCEEDED" {
				wantLimit := "program_bytes"
				if sourced {
					wantLimit = "source_bytes"
				}
				if rejection.Data["limit_id"] != wantLimit || rejection.Data["resource_unit"] != "bytes" {
					t.Fatalf("resource feedback lacks typed limit facts (sourced=%t): %+v", sourced, rejection.Data)
				}
			}
		}
	}
}
