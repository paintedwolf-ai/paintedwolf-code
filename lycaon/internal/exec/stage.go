package exec

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
)

// Stage is one argv vector in a shell-free command plan, with the operator that
// joins it to the stage before it.
type Stage struct {
	Name string
	Args []string
	// Globs holds each argument's unexpanded glob pattern, aligned with Args.
	// The command boundary expands them before approval; the executor passes
	// Args as they stand.
	Globs []string `json:"globs,omitempty"`
	// Addressed marks each argument that begins with an unquoted `@`, aligned
	// with Args. The command boundary resolves those addresses before approval.
	Addressed []bool            `json:"addressed,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	// Connector joins the preceding stage; sequencing operators wait for its group's exit.
	Connector argv.Connector
	// Canonical is the reference-bearing display form. Empty uses CommandLine.
	Canonical string `json:"-"`
	// Redirects are the stage's redirections as written; Streams is their effect.
	Redirects []argv.Redirect `json:"redirects,omitempty"`
	Streams   argv.Streams    `json:"streams,omitzero"`
}

// EchoLine returns the reference-safe display form.
func (s Stage) EchoLine() string {
	if s.Canonical != "" {
		return s.Canonical
	}
	return s.CommandLine()
}

// CommandLine renders the stage, redirections included, so it parses back to
// itself: an argument that begins with `@` stays an address only when marked.
func (s Stage) CommandLine() string {
	addressed := s.Addressed
	if addressed == nil {
		addressed = []bool{}
	}
	line := argv.JoinAddressedCommandLine(s.Env, s.Name, s.Args, addressed)
	if len(s.Redirects) == 0 {
		return line
	}
	return line + " " + argv.RenderRedirects(s.Redirects)
}

// StageGroup is a half-open range of pipe-connected stages that run together.
type StageGroup struct {
	Start int
	End   int
	// Connector is the operator joining this group to the previous one, taken from
	// the group's first stage. The first group carries argv.ConnectorNone.
	Connector argv.Connector
}

// Len reports how many stages the group covers.
func (g StageGroup) Len() int { return g.End - g.Start }

// Final reports whether stage i ends its group, so its default stdout is the call's output.
func (g StageGroup) Final(i int) bool { return i == g.End-1 }

// GroupStages opens a new group at each sequencing connector.
func GroupStages(stages []Stage) []StageGroup {
	var groups []StageGroup
	for i, stage := range stages {
		if i == 0 || stage.Connector.Sequencing() {
			groups = append(groups, StageGroup{Start: i, End: i + 1, Connector: stage.Connector})
			continue
		}
		groups[len(groups)-1].End = i + 1
	}
	return groups
}

// ValidateStreams refuses redirections a pipe group cannot honour: a piped
// stage that sends nothing into its pipe, and a piped stage that names its own stdin.
func ValidateStreams(stages []Stage) error {
	for _, group := range GroupStages(stages) {
		for i := group.Start; i < group.End; i++ {
			streams := stages[i].Streams
			if !group.Final(i) && !streams.FeedsStdout() {
				return &argv.RedirectionError{Issue: argv.IssuePipeStdout, Operator: stages[i].CommandLine()}
			}
			if i > group.Start && streams.Stdin != (argv.Source{}) {
				return &argv.RedirectionError{Issue: argv.IssuePipeStdin, Operator: stages[i].CommandLine()}
			}
		}
	}
	return nil
}

// StagesFromCommandLine parses a command line into the stages it runs. Sequencing
// operators (`&&`, `||`, `;`) and pipes yield one stage per element.
func StagesFromCommandLine(line string) ([]Stage, error) {
	elements, err := argv.SplitSequence(line)
	if err != nil {
		return nil, err
	}
	stages := make([]Stage, len(elements))
	for i, el := range elements {
		stages[i] = stageFromElement(el)
	}
	for i := 0; i < len(stages)-1; i++ {
		if stages[i+1].Connector == argv.ConnectorPipeMerged {
			// `a |& b` is `a 2>&1 | b`, applied after a's own redirections.
			stages[i].Streams.Stderr = stages[i].Streams.StdoutSink()
		}
	}
	return stages, nil
}

// StagesFromCommandLines requires one command per pipeline element; sequences have no single output stream.
func StagesFromCommandLines(lines []string) ([]Stage, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("pipeline requires at least one stage")
	}
	out := make([]Stage, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, fmt.Errorf("pipeline stage %d is empty", i)
		}
		el, err := argv.SplitElement(line)
		if err != nil {
			return nil, fmt.Errorf("pipeline stage %d: %w", i, err)
		}
		el.Connector = argv.ConnectorPipe
		if i == 0 {
			el.Connector = argv.ConnectorNone
		}
		out = append(out, stageFromElement(el))
	}
	return out, nil
}

func stageFromElement(el argv.SequenceElement) Stage {
	return Stage{
		Name:      el.Name,
		Args:      el.Args,
		Globs:     el.Globs,
		Addressed: el.Addressed,
		Env:       el.Env,
		Connector: el.Connector,
		Redirects: el.Redirects,
		Streams:   el.Streams,
	}
}
