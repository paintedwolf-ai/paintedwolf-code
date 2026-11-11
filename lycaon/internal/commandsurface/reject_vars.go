package commandsurface

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
)

// CommandRejectVars builds schema-valid command rejection context.
func CommandRejectVars(profileID, displayCommand, rawCommand string, err error) map[string]any {
	metachar := errors.Is(err, argv.ErrShellMetacharacters)
	var syntax *argv.MetacharacterError
	errors.As(err, &syntax)

	// A here-document runs under the command tool's program grammar.
	wantStdin := syntax != nil && syntax.Char == "<<"
	reissueViaCommand := errors.Is(err, ErrSequenceUnsupported) ||
		errors.Is(err, argv.ErrBackgroundOperator) ||
		wantStdin ||
		(metachar && argv.HasUnquotedPipe(rawCommand))

	return map[string]any{
		"command":              strings.TrimSpace(displayCommand),
		"profile_id":           profileID,
		"reason":               err.Error(),
		"shell_metacharacters": metachar,
		"scratch_var":          syntax != nil && syntax.Var == exec.ScratchDirEnv,
		"env_assignment":       errors.Is(err, argv.ErrEnvAssignmentCommand),
		"directory_change":     errors.Is(err, ErrDirectoryChangeCommand),
		"sequence_unsupported": errors.Is(err, ErrSequenceUnsupported),
		"reissue_via_command":  reissueViaCommand,
	}
}
