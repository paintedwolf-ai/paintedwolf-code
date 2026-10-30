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
	for _, source := range []string{"printf ok; if", "if false; then eval unknown; fi", "printf ok; echo ${(q)name}", strings.Repeat(" ", 65537)} {
		_, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "fixture.pw"})
		rejection := toolcommand.ProgramSyntaxObservation("command", err)
		if rejection == nil {
			t.Fatalf("missing typed program feedback: %v", err)
		}
		out, err := formatter.Format(rejection.Code, rejection.Data)
		contractcheck.FailErr(t, "render syntax feedback", err)
		for _, want := range []string{"Code: " + rejection.Code, "earlier parent effects"} {
			if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
				t.Fatalf("feedback missing %q: %s", want, out)
			}
		}
		if strings.Contains(out, "replacement_args") || strings.Contains(out, "sh -c") {
			t.Fatalf("invented repair or fallback: %s", out)
		}
	}
}
