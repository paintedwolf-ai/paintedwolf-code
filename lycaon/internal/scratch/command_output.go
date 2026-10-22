package scratch

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const commandOutputDirectory = "output"

// PrepareCommandOutputDirectory creates the protected output directory before
// an external launch, including when no retained output exists yet.
func PrepareCommandOutputDirectory(sessionDir string) (string, error) {
	if err := fseffect.MkdirAll(fseffect.Location{Root: sessionDir, Rel: commandOutputDirectory}, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(sessionDir, commandOutputDirectory), nil
}

// PublishCommandOutput installs screened retained bytes in a session's output
// directory. The caller supplies the session capability and owns retention.
func PublishCommandOutput(sessionDir, programID, stream string, content io.Reader) error {
	if programID == "" || filepath.Base(programID) != programID || strings.ContainsAny(programID, `/\`) || programID == "." || programID == ".." || (stream != "stdout" && stream != "stderr") {
		return fseffect.ErrInvalidPath
	}
	if _, err := PrepareCommandOutputDirectory(sessionDir); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: sessionDir, Rel: filepath.Join(commandOutputDirectory, programID+"."+stream)},
		Source:   content, Mode: 0o444, DirMode: 0o700,
	})
	return err
}
