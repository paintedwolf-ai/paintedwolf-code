package gitcmd

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Resolve names the catalog Git command an argv invokes: global options
// (-C DIR, -c KEY=VALUE, --no-pager) are skipped and the longest catalogued
// subcommand wins, so git -C DIR stash list is git stash list.
func (o *Owners) Resolve(argv commandprogram.Fields) (string, bool) {
	i := 1
	for i < len(argv) {
		word := argv[i].Value
		switch {
		case word == "-C" || word == "-c":
			i += 2
			continue
		case strings.HasPrefix(word, "-C") || strings.HasPrefix(word, "-c") || word == "--no-pager":
			i++
			continue
		}
		break
	}
	best := ""
	for _, spec := range o.catalog.Commands {
		names := strings.Fields(spec.Name)
		if len(names) < 2 || names[0] != "git" || i+len(names)-1 > len(argv) || len(spec.Name) <= len(best) {
			continue
		}
		match := true
		for k, name := range names[1:] {
			match = match && argv[i+k].Value == name
		}
		if match {
			best = spec.Name
		}
	}
	return best, best != ""
}

// PrepareGit serves the git umbrella command: it resolves the subcommand
// and binds it, or refuses a Git form these owners do not run. nested runs
// git bisect run's check as a child of this request; nil disables run.
func (o *Owners) PrepareGit(paths *projectpaths.ProgramPaths, filesystem *mutation.Owners, nested commandprogram.NestedCommand, request commandprogram.OperationRequest) (Prepared, error) {
	name, ok := o.Resolve(request.Argv)
	if _, served := o.specs[name]; !ok || !served {
		return Prepared{}, o.unsupported(name, request)
	}
	request.OperationID = name
	prepared, err := o.Prepare(paths, filesystem, request)
	prepared.nested = nested
	return prepared, err
}

// unsupported refuses a Git form with the forms this command language runs.
func (o *Owners) unsupported(name string, request commandprogram.OperationRequest) error {
	word := "git"
	if len(request.Argv) > 1 {
		word = request.Argv[1].Value
	}
	if name != "" {
		word = name
	}
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{
		"command": "git", "word": word, "expected": "a supported Git form: " + strings.Join(o.Commands(), ", "),
		"source_start_byte": request.Invocation.Span.StartByte, "source_end_byte": request.Invocation.Span.EndByte,
		"source_line": request.Invocation.Span.Line, "source_column": request.Invocation.Span.Column,
	}}
}
