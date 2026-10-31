package commandsurface

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ErrSessionUnavailable marks a missing host session folder.
var ErrSessionUnavailable = errors.New("session folder is unavailable")

// SessionOperandError names the argument that could not be resolved.
type SessionOperandError struct {
	Operand string
	Area    string
	Err     error
}

func (e *SessionOperandError) Error() string {
	return fmt.Sprintf("session argument %q: %v", e.Operand, e.Err)
}

func (e *SessionOperandError) Unwrap() error { return e.Err }

// ResolveSessionOperands gives unquoted session addresses their real host paths.
// Quoted names stay literal; addressed globs keep their rooted patterns.
func ResolveSessionOperands(stages []exec.Stage, scratchDir, outputDir string) ([]exec.Stage, bool, error) {
	out := make([]exec.Stage, len(stages))
	changed := false
	for i, stage := range stages {
		out[i] = stage
		if len(stage.Addressed) == 0 {
			continue
		}
		args := append([]string(nil), stage.Args...)
		addressed := append([]bool(nil), stage.Addressed...)
		globs := append([]string(nil), stage.Globs...)
		for j, arg := range stage.Args {
			if j >= len(addressed) || !addressed[j] {
				continue
			}
			area, rel, ok := projectroot.SessionAddress(arg)
			if !ok {
				continue
			}
			dir := scratchDir
			if area == projectroot.VirtualOutputLabel {
				dir = outputDir
			}
			if strings.TrimSpace(dir) == "" {
				return nil, false, &SessionOperandError{Operand: arg, Area: area, Err: ErrSessionUnavailable}
			}
			abs, err := projectroot.SessionPath(dir, rel)
			if err != nil {
				return nil, false, &SessionOperandError{Operand: arg, Area: area, Err: err}
			}
			args[j], addressed[j] = abs, false
			if j < len(globs) && globs[j] != "" {
				globs[j] = rootedSessionPattern(globs[j], dir)
			}
			changed = true
		}
		out[i].Args, out[i].Addressed = args, addressed
		if stage.Globs != nil {
			out[i].Globs = globs
		}
	}
	return out, changed, nil
}

// rootedSessionPattern re-roots an addressed glob at its host folder.
// The label carries no glob character, so the pattern repeats it as written.
func rootedSessionPattern(pattern, directory string) string {
	_, rest, _ := strings.Cut(pattern, "/")
	return argv.EscapeGlob(strings.TrimSuffix(directory, "/")) + "/" + rest
}
