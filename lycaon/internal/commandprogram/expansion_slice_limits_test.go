package commandprogram

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSubstringLengthCannotOverflowItsSourceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, program, want string
	}{
		{"remaining text", `v=abc; echo "${v:1:9223372036854775807}"`, "echo bc"},
		{"complete text", `v=abc; echo "${v:0:9223372036854775807}"`, "echo abc"},
		{"offset beyond text", `v=abc; echo "${v:9223372036854775807:9223372036854775807}"`, "echo "},
		{"Unicode scalars", `v=αβγ; echo "${v:1:9223372036854775807}"`, "echo βγ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t)
			out, err := f.run(t, t.Context(), tc.program)
			if err != nil || out.Status != 0 {
				t.Fatalf("substring outcome=%+v error=%v", out, err)
			}
			if got := f.log.argv(); !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("substring command=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSubstringAdmitsSourceDecodingBeforeReturningSmallResult(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 256, State: 1 << 20})
	testutil.FailErr(t, "small substring working budget", err)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, []string{"V=" + strings.Repeat("x", 512)})
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: "${V:1:1}"`)[1])
	var exhausted *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &exhausted) || exhausted.Account != "working" {
		t.Fatalf("substring error=%v, want working memory refusal", err)
	}
	if used := memory.InUse(); used.Working != 0 {
		t.Fatalf("refused substring retained working storage: %+v", used)
	}
}
