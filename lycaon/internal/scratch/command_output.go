package scratch

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// CommandOutput identifies a program stream or one of its child streams.
type CommandOutput struct {
	ProgramID   string
	ExecutionID string
	Stream      string
}

func (o CommandOutput) Path() (string, error) {
	valid := func(id string) bool { return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`) }
	if !valid(o.ProgramID) || (o.ExecutionID != "" && !valid(o.ExecutionID)) || (o.Stream != "stdout" && o.Stream != "stderr") {
		return "", fseffect.ErrInvalidPath
	}
	if o.ExecutionID != "" {
		return o.ProgramID + "/" + o.ExecutionID + "." + o.Stream, nil
	}
	return o.ProgramID + "." + o.Stream, nil
}

// PublishCommandOutput installs screened bytes in the session's read-only view.
func PublishCommandOutput(outputDir string, output CommandOutput, content io.Reader) error {
	path, err := output.Path()
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: outputDir, Rel: filepath.FromSlash(path)},
		Source:   content, Mode: 0o444, DirMode: 0o700,
	})
	return err
}
