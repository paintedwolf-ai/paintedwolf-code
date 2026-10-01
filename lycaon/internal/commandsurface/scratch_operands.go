package commandsurface

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ErrScratchUnavailable reports an @scratch argument in an invocation that has
// no session scratch folder.
var ErrScratchUnavailable = errors.New("session scratch is unavailable")

// ScratchOperandError names the argument that could not be resolved.
type ScratchOperandError struct {
	Operand string
	Err     error
}

func (e *ScratchOperandError) Error() string {
	return fmt.Sprintf("scratch argument %q: %v", e.Operand, e.Err)
}

func (e *ScratchOperandError) Unwrap() error { return e.Err }

// ResolveScratchOperands replaces each unquoted `@scratch/...` argument with
// its absolute path in the session's scratch folder, so any program receives
// a path it can open. A quoted `@scratch/...` stays literal, and another `@`
// word, such as a package scope, is left as written. A glob under an address
// keeps its pattern, rooted at the folder.
func ResolveScratchOperands(stages []exec.Stage, scratchDir string) ([]exec.Stage, bool, error) {
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
			rel, ok := projectroot.ScratchAddress(arg)
			if !ok {
				continue
			}
			if strings.TrimSpace(scratchDir) == "" {
				return nil, false, &ScratchOperandError{Operand: arg, Err: ErrScratchUnavailable}
			}
			abs, err := projectroot.ScratchPath(scratchDir, rel)
			if err != nil {
				return nil, false, &ScratchOperandError{Operand: arg, Err: err}
			}
			args[j], addressed[j] = abs, false
			if j < len(globs) && globs[j] != "" {
				globs[j] = rootedScratchPattern(globs[j], scratchDir)
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

// rootedScratchPattern re-roots an `@scratch/<pattern>` glob at the folder.
// The label carries no glob character, so the pattern repeats it as written.
func rootedScratchPattern(pattern, scratchDir string) string {
	_, rest, _ := strings.Cut(pattern, "/")
	return argv.EscapeGlob(strings.TrimSuffix(scratchDir, "/")) + "/" + rest
}
