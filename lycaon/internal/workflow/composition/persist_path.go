package composition

import (
	"fmt"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"path/filepath"
	"regexp"
	"strings"
)

func projectWorkflowsDir() string { return settingsoverlay.Rel("workflows") }

var persistIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,48}$`)

// ResolvePersistFeatureDir returns the feature directory name a persisted
// workflow occupies under <overlay>/workflows/. The id is the whole destination:
// one feature dir holding one workflow.yaml, as in bundled packs.
func ResolvePersistFeatureDir(workflowID string) (string, error) {
	workflowID = strings.TrimSpace(workflowID)
	if !persistIDPattern.MatchString(workflowID) {
		return "", fmt.Errorf("workflow id %q must match ^[a-z][a-z0-9-]{1,48}$", workflowID)
	}
	return workflowID, nil
}

// ProjectWorkflowOverlayPath returns the project-relative manifest path
// (<overlay>/workflows/<id>/workflow.yaml).
func ProjectWorkflowOverlayPath(featureDir string) string {
	return filepath.ToSlash(filepath.Join(projectWorkflowsDir(), featureDir, config.WorkflowManifestName))
}
