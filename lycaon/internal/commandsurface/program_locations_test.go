package commandsurface_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestExecutionLocationsReachEveryStaticStartOnce(t *testing.T) {
	source := `out=$(echo assign)
echo "$(echo quoted)" > "$(echo target)"
echo ${missing:-$(echo default)}
for item in $(echo items); do echo body; done
case $(echo word) in $(echo pattern)) echo matched;; esac
if $(echo cond); then echo then; else echo else; fi
while $(echo loop); do break; done
helper() { echo $(echo function); }
{ echo grouped; } && ( echo isolated ) | cat
time echo $(echo timed) | cat
echo {$(echo brace),plain}
`
	locations := commandsurface.ExecutionLocations(parseProgram(t, source).Statements)
	texts := make([]string, 0, len(locations))
	ids := map[commandsurface.NodeID]bool{}
	for _, location := range locations {
		if ids[location.ID] {
			t.Fatalf("location %s listed twice", location.ID)
		}
		ids[location.ID] = true
		texts = append(texts, source[location.Span.StartByte:location.Span.EndByte])
	}
	if len(texts) == 0 || texts[0] != "out=$(echo assign)" {
		t.Fatalf("first location = %q, want the first statement", texts)
	}
	for _, want := range []string{
		"$(echo assign)", "echo assign",
		"$(echo quoted)", `> "$(echo target)"`, "$(echo target)", "echo target",
		"$(echo default)", "echo default",
		"$(echo items)", "echo body;",
		"$(echo word)", "$(echo pattern)", "echo matched",
		"$(echo cond)", "echo then;", "echo else;",
		"$(echo loop)", "break;",
		"$(echo function)", "echo function",
		"echo grouped;", "echo isolated", "cat",
		"echo $(echo timed)", "$(echo timed)", "echo timed",
		"$(echo brace)", "echo brace",
	} {
		if !slices.Contains(texts, want) {
			t.Fatalf("no location for %q in %q", want, texts)
		}
	}
	if got := commandsurface.ExecutionLocations(nil); len(got) != 0 {
		t.Fatalf("empty program locations = %v", got)
	}
}
