package hostcmd

import "github.com/lycaon/lycaon/internal/exec"

// IOParams carries optional pipeline stdin, env, and redirection settings.
type IOParams struct {
	Stdin         *exec.StdinSpec
	InlineEnv     map[string]string
	Redirect      *exec.RedirectSpec
	StdinProvided bool
	StdinFrom     string
	StdoutTo      string
	StderrTo      string
	EnvKeys       []string
}
