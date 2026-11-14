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

// ParseCommandOutput accepts only canonical relative output names.
func ParseCommandOutput(path string) (CommandOutput, bool) {
	var out CommandOutput
	parts := strings.Split(path, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return out, false
	}
	file := parts[len(parts)-1]
	dot := strings.LastIndexByte(file, '.')
	if dot < 1 {
		return out, false
	}
	out.ProgramID, out.Stream = file[:dot], file[dot+1:]
	if len(parts) == 2 {
		out.ProgramID, out.ExecutionID = parts[0], file[:dot]
	}
	canonical, err := out.Path()
	return out, err == nil && canonical == path
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
