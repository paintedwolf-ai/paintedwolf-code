package guard

import (
	"sync"

	"github.com/lycaon/lycaon/internal/oar"
)

var (
	hostFinishPipelineOnce sync.Once
	hostFinishPipeline     *oar.GuardPipeline
)
