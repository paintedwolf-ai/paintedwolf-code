package native

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
)

// BlueprintWriteObserver is notified after a successful native write to a project-relative path.
type BlueprintWriteObserver interface {
	AfterWrite(ctx context.Context, sessionID, relPath string)
}

var (
	blueprintWriteMu       sync.RWMutex
	blueprintWriteObserver BlueprintWriteObserver
)

// SetBlueprintWriteObserver wires the host observer (nil clears).
func SetBlueprintWriteObserver(o BlueprintWriteObserver) {
	blueprintWriteMu.Lock()
	blueprintWriteObserver = o
	blueprintWriteMu.Unlock()
}

func notifyBlueprintWrite(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	blueprintWriteMu.RLock()
	o := blueprintWriteObserver
	blueprintWriteMu.RUnlock()
	if o == nil {
		return
	}
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		o.AfterWrite(ctx, sessionID, p)
	}
}
