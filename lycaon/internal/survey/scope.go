package survey

import (
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
)

// Scope is the workspace context for survey probe execution.
type Scope struct {
	Boundary      *sandbox.Boundary
	ToolCtx       tools.ToolContext
	SourceCatalog *sourcecatalog.Catalog
	ReadFilter    sandbox.ReadFilter
}
