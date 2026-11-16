package settings

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ErrProjectSettingsNeedRepair refuses a write over a project settings file the
// host could not read: saving would replace what the person wrote.
var ErrProjectSettingsNeedRepair = errors.New("project settings file could not be read; fix it before saving")

// ProjectSettingsUnreadable is the shared overlay rejection code for a file
// none of which applied.
const ProjectSettingsUnreadable = "project_unreadable"

// ProjectRejection is a project settings file the host did not apply.
type ProjectRejection struct {
	Detail string
}

// readProjectFile loads one project settings file. A missing file is absent;
// a file that cannot be read or decoded is rejected, never silently skipped.
func readProjectFile[T any](projectDir, basename string, load func(path string) (T, error)) (T, bool, *ProjectRejection) {
	value, err := load(settingsoverlay.ProjectOverlayPath(projectDir, basename))
	switch {
	case err == nil:
		return value, true, nil
	case errors.Is(err, fs.ErrNotExist):
		var zero T
		return zero, false, nil
	default:
		var zero T
		return zero, false, &ProjectRejection{Detail: fmt.Sprintf("%s: %v", settingsoverlay.Rel(basename), err)}
	}
}

// decodeProjectFile decodes strictly, so a misspelled key is refused rather
// than ignored. An empty file sets nothing.
func decodeProjectFile[T any](data []byte, target *T) error {
	if err := config.DecodeYAML(data, target); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func rejections(r *ProjectRejection) []ProjectRejection {
	if r == nil {
		return nil
	}
	return []ProjectRejection{*r}
}
