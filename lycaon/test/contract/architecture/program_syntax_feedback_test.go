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

// Program syntax feedback names the refusal by code and locates it by the
// parser's span, as top-level or sourced input, and carries no guessed repair.
func TestProgramSyntaxFeedbackUsesTypedSpansWithoutGuessedRepairs(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load program syntax feedback", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	for _, sourced := range []bool{false, true} {
		oversized := strings.Repeat(" ", int(commandsurface.SyntaxUnitByteLimit(sourced))+1)
		for _, tc := range []struct {
			name, source, code    string
			start, end, line, col int
		}{
			{"unfinished if", "printf ok; if", "COMMAND_SYNTAX_INVALID", 11, 12, 1, 12},
			{"unknown trap", "if false; then trap unknown; fi", "COMMAND_SYNTAX_UNSUPPORTED", 15, 19, 1, 16},
			{"zsh flag expansion", "printf ok; echo ${(q)name}", "COMMAND_SYNTAX_UNSUPPORTED", 16, 17, 1, 17},
			{"oversized", oversized, "COMMAND_LIMIT_EXCEEDED", 0, 0, 1, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := commandsurface.ParseProgram([]byte(tc.source), commandsurface.ParseOptions{SourceName: "fixture.pw", Sourced: sourced})
				rejection := toolcommand.ProgramSyntaxObservation("command", err)
				if rejection == nil || rejection.Code != tc.code {
					t.Fatalf("sourced=%t: rejection = %+v, want %s (parse error %v)", sourced, rejection, tc.code, err)
				}
				span := map[string]any{"source_start_byte": tc.start, "source_end_byte": tc.end, "source_line": tc.line, "source_column": tc.col}
				for key, want := range span {
					if rejection.Data[key] != want {
						t.Fatalf("sourced=%t: %s = %v, want %v (data %+v)", sourced, key, rejection.Data[key], want, rejection.Data)
					}
				}
				for key := range rejection.Data {
					if strings.Contains(key, "repair") || strings.Contains(key, "suggest") {
						t.Fatalf("sourced=%t: feedback guesses a repair in %q: %+v", sourced, key, rejection.Data)
					}
				}
				if tc.code == "COMMAND_LIMIT_EXCEEDED" {
					wantLimit := "program_bytes"
					if sourced {
						wantLimit = "source_bytes"
					}
					if rejection.Data["limit_id"] != wantLimit || rejection.Data["resource_unit"] != "bytes" {
						t.Fatalf("sourced=%t: limit facts = %+v, want %s in bytes", sourced, rejection.Data, wantLimit)
					}
				}
				out, err := formatter.Format(rejection.Code, rejection.Data)
				contractcheck.FailErr(t, "render syntax feedback", err)
				if !strings.Contains(out, "Code: "+rejection.Code) {
					t.Fatalf("sourced=%t: feedback lost its code: %s", sourced, out)
				}
			})
		}
	}
}
