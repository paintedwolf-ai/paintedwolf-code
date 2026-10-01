package llm

import (
	"github.com/lycaon/lycaon/internal/llm/compaction"
)

func testCompactionConfig() compaction.CompactionConfig {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	return cfg
}
