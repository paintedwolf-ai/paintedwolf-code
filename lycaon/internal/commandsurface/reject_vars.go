package commandsurface

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
)

// CommandRejectVars builds schema-valid command rejection context.
func CommandRejectVars(profileID, displayCommand, rawCommand string, argFields []string, err error) map[string]any {
	metachar := errors.Is(err, argv.ErrShellMetacharacters)
	var syntax *argv.MetacharacterError
	errors.As(err, &syntax)
	carries := fieldSet(argFields)

	wantPipeline := metachar && argv.HasUnquotedPipe(rawCommand)
	// A here-document is the one stdin form the grammar leaves to the stdin field.
	wantStdin := syntax != nil && syntax.Char == "<<"
	wantBackground := errors.Is(err, argv.ErrBackgroundOperator)
	sequenceUnsupported := errors.Is(err, ErrSequenceUnsupported)
	envAssignment := errors.Is(err, argv.ErrEnvAssignmentCommand)

	usePipeline := wantPipeline && carries["pipeline"]
	useStdin := wantStdin && carries["stdin"]
	useBackground := wantBackground && carries["background"]

	return map[string]any{
		"command":              strings.TrimSpace(displayCommand),
		"profile_id":           profileID,
		"reason":               err.Error(),
		"shell_metacharacters": metachar,
		"use_pipeline_array":   usePipeline,
		"use_stdin_param":      useStdin,
		// Substitutions resolve before structured execution.
		"use_substitution_result": syntax != nil && strings.ContainsAny(syntax.Char, argv.SubstitutionChars),
		"scratch_var":             syntax != nil && syntax.Var == exec.ScratchDirEnv,
		"use_background_param":    useBackground,
		"env_assignment":          envAssignment,
		"sequence_unsupported":    sequenceUnsupported,
		"reissue_via_command": sequenceUnsupported ||
			(wantPipeline && !usePipeline) ||
			(wantStdin && !useStdin) ||
			(wantBackground && !useBackground),
	}
}

// fieldSet indexes accepted tool arguments.
func fieldSet(argFields []string) map[string]bool {
	out := make(map[string]bool, len(argFields))
	for _, name := range argFields {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}
