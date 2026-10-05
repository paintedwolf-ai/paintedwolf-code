package definition

import "fmt"

// MaxManifestFormat is the highest workflow manifest document schema version
// supported by this host.
const MaxManifestFormat = 1

// ValidateManifestFormat verifies that the requested format version is supported.
func ValidateManifestFormat(workflowID string, format int) error {
	if format < 1 || format > MaxManifestFormat {
		return fmt.Errorf("WORKFLOW_FORMAT_UNSUPPORTED: workflow %q requires manifest format %d, but this host only supports formats up to %d", workflowID, format, MaxManifestFormat)
	}
	return nil
}

// ApplyFormatDefaults applies explicit defaults for the declared or inferred
// manifest format to ensure downstream consumers observe normalized values.
func ApplyFormatDefaults(wf *workflowFile) {
	if wf == nil {
		return
	}
	if wf.Format <= 0 {
		wf.Format = 1
	}
}
