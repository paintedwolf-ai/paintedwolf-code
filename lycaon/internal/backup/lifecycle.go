package backup

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

func retainCaptureBodies(dataDir string) func() {
	sourceRelease := sourceblob.New(filepath.Join(dataDir, enginepaths.SourceContentDirName)).AcquireReferenceLease()
	contentRelease := bloblifecycle.AcquirePublication(dataDir)
	return func() { contentRelease(); sourceRelease() }
}
