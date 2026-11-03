package checkpoint

import (
	"github.com/lycaon/lycaon/internal/layeredfs"
	"os"
)

// promotionTargets owns descriptor-confined reads, staged writes and namespace effects.
// Publication hooks are installed and read only under Promoter.applyMu.
type promotionTargets struct {
	store           *Store
	roots           map[string]string
	recordPostimage func(layeredfs.QualifiedPath, *os.File) error
}
