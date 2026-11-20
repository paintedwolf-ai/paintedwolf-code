package commandconformance

import (
	"fmt"
	"strconv"
	"strings"
)

// statusSources settle with distinct statuses through each kind of owner. They
// write nothing, because a job's output goes to its own streams in the command
// entry and a shell's job writes to the shell's. ownerKind marks the first
// source of each kind of job owner; the others repeat a kind with another
// command.
var statusSources = []struct {
	text      string
	ownerKind bool
}{
	{`true`, true}, // builtin
	{`false`, false},
	{`(exit 3)`, true}, // subshell
	{`test -f missing.txt`, false},
	{`grep -q absent notes.txt`, true}, // native utility
	{`cat missing.txt 2>/dev/null`, false},
	{`git status >/dev/null 2>&1`, true}, // native Git
	{`sh -c 'exit 5'`, true},             // external program
}

// statusCompositions place each source, written {s}, in every construct that
// decides a status from its parts.
var statusCompositions = []struct {
	id, description string
	templates       []string
}{
	{"pipeline-stages", "A source as each pipeline stage.", []string{
		`{s} | cat`, `: | {s}`, `: | {s} | cat`, `{s} | false`, `false | {s}`, `{ {s}; } | cat`,
	}},
	{"pipeline-external", "A source beside an external stage.", []string{
		`{s} | sh -c 'cat'`, `sh -c ':' | {s}`,
	}},
	{"pipeline-negated", "A negated pipeline.", []string{
		`! {s} | cat`, `! : | {s}`,
	}},
	{"lists", "A source in a negation or a list.", []string{
		`! {s}`, `{s} && :`, `{s} || :`, `{s} && false`, `{s} || (exit 4)`,
	}},
	{"frames", "A source in a group, subshell, function or substitution.", []string{
		`{ {s}; }`, `( {s} )`, `f() { {s}; }; f`, `v=$( {s} )`,
	}},
	{"frames-pipeline", "A pipeline inside a group, subshell or function.", []string{
		`{ {s} | true; }`, `( {s} | true )`, `f() { {s} | true; }; f`,
	}},
	{"branches", "A source as a condition or a branch.", []string{
		`if {s}; then :; fi`, `if {s}; then false; else (exit 4); fi`, `case x in x) {s};; esac`, `{s}; case x in y) :;; esac`,
	}},
	{"loops", "A source as a loop condition or body.", []string{
		`while {s}; do break; done`, `until {s}; do break; done`, `for v in x; do {s}; done`, `{s}; for v in; do :; done`,
	}},
	{"declarations", "A function declaration keeps the last command's pipeline statuses.", []string{
		`{s}; g() { :; }`,
	}},
	{"jobs-started", "Starting a job does not report its status.", []string{
		`{s} & :`, `{s} &`,
	}},
	{"wait-bare", "A wait with no operands reports 0 for any job status.", []string{
		`{s} & wait`, `{s} & true & wait`, `true & {s} & wait`, `{s} | cat & wait`, `{s} & wait $!; wait`,
	}},
	{"wait-named", "A wait on a job reports that job's status.", []string{
		`{s} & wait $!`, `{s} & wait %%`, `{s} | cat & wait $!`, `{ {s}; } & wait $!`, `( {s} ) & wait $!`,
	}},
	{"wait-operands", "A wait on several jobs reports its last operand.", []string{
		`{s} & a=$!; true & b=$!; wait "$a" "$b"`, `{s} & a=$!; true & b=$!; wait "$b" "$a"`,
	}},
	{"wait-again", "A wait on a reaped job repeats its status, however many jobs ran since.", []string{
		`{s} & wait $!; wait $!`, `{s} & a=$!; wait "$a"; for i in {1..20}; do true & wait $!; done; wait "$a"`,
	}},
	{"wait-in-lists", "A wait's status decides a list and a negation.", []string{
		`{s} & wait && (exit 6)`, `{s} & wait || (exit 6)`, `{s} & wait $! && (exit 6)`, `{s} & wait $! || (exit 6)`, `{s} & ! wait $!`,
	}},
	{"wait-in-substitution", "A substitution waits for its own job.", []string{
		`v=$( {s} & wait )`, `v=$( {s} & wait $! )`,
	}},
}

// StatusCases compares the status algebra with bash exhaustively, so a construct
// is covered whichever commands other cases happen to pair it with. A wait after
// each report keeps the program within the host's concurrent job limit.
func StatusCases() []Case {
	cases := make([]Case, 0, len(statusCompositions))
	for _, composition := range statusCompositions {
		cases = append(cases, Case{
			ID:          statusCaseID(composition.id),
			Description: composition.description,
			Program:     statusProgram(composition.templates, func(int) bool { return true }),
			References:  gnuUserland,
		})
	}
	return cases
}

func statusCaseID(composition string) string { return "status-" + composition }

// statusProgram reports every kept source in every template. A report names
// its template and source by their places in the full corpus, so a reduced
// program's output is a subset of the full program's.
func statusProgram(templates []string, keep func(source int) bool) string {
	var program strings.Builder
	for t, template := range templates {
		for s, source := range statusSources {
			if !keep(s) {
				continue
			}
			fmt.Fprintf(&program, "%s\nprintf '%d.%d status=%%s pipe=%%s\\n' \"$?\" \"${PIPESTATUS[*]}\"\nwait\n", strings.ReplaceAll(template, "{s}", source.text), t, s)
		}
	}
	program.WriteString("wait\nprintf 'end status=%s\\n' \"$?\"\n")
	return program.String()
}

// OwnerKindStatus is a status case cut to one source of each kind of job
// owner. The host runs it where the evaluator's table already covers every
// source, and compares it with the same cut of the full case's reference.
type OwnerKindStatus struct {
	Case Case
}

// OwnerKindStatusOf reduces a status case; ok is false for any other case.
func OwnerKindStatusOf(c Case) (reduced OwnerKindStatus, ok bool) {
	for _, composition := range statusCompositions {
		if statusCaseID(composition.id) != c.ID {
			continue
		}
		c.Program = statusProgram(composition.templates, func(s int) bool { return statusSources[s].ownerKind })
		return OwnerKindStatus{Case: c}, true
	}
	return OwnerKindStatus{}, false
}

// Reference keeps the full reference's reports of owner-kind sources and its
// final status line.
func (OwnerKindStatus) Reference(full Snapshot) Snapshot {
	var kept strings.Builder
	for _, line := range strings.SplitAfter(string(full.Stdout), "\n") {
		place, _, _ := strings.Cut(line, " ")
		_, source, found := strings.Cut(place, ".")
		index, err := strconv.Atoi(source)
		if found && err == nil && index < len(statusSources) && !statusSources[index].ownerKind {
			continue
		}
		kept.WriteString(line)
	}
	full.Stdout = Text(kept.String())
	return full
}
