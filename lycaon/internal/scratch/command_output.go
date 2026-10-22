package scratch

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// PublishCommandOutput installs screened retained bytes in a session's output
// directory. The caller supplies the session capability and owns retention.
func PublishCommandOutput(sessionDir, programID, stream string, content io.Reader) error {
	if programID == "" || filepath.Base(programID) != programID || strings.ContainsAny(programID, `/\`) || programID == "." || programID == ".." || (stream != "stdout" && stream != "stderr") {
		return fseffect.ErrInvalidPath
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: sessionDir, Rel: filepath.Join("output", programID+"."+stream)},
		Source:   content, Mode: 0o444, DirMode: 0o700,
	})
	return err
}
