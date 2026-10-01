package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// OutOfSessionScope reports whether the tool's declared session scope excludes
// the invoking session. The schema already withholds these, so true here means
// a stale or invented call.
func OutOfSessionScope(name string, tctx ToolContext) bool {
	workerChild := strings.TrimSpace(tctx.ParentSessionID) != ""
	return !toolcontract.AdmitsSession(strings.TrimSpace(name), workerChild)
}
