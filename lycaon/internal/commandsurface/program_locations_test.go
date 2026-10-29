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
echo {first,$(echo brace)}
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
	} {
		if !slices.Contains(texts, want) {
			t.Fatalf("no location for %q in %q", want, texts)
		}
	}
	// Nodes inside brace alternatives carry the whole brace word's span, so the
	// substitution and its statement both appear as that word.
	if got := countOf(texts, "{first,$(echo brace)}"); got != 2 {
		t.Fatalf("brace alternative locations = %d, want the substitution and its statement", got)
	}
	if got := commandsurface.ExecutionLocations(nil); len(got) != 0 {
		t.Fatalf("empty program locations = %v", got)
	}
}

func countOf(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}
