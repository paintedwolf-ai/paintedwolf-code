package native

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workscope"
)

// BlueprintWriteObserver is notified after a successful native write to a project-relative path.
type BlueprintWriteObserver interface {
	AfterWrite(ctx context.Context, sessionID, relPath string)
}

var (
	blueprintWriteMu       sync.RWMutex
	blueprintWriteObserver *blueprintRegistration
)

type blueprintRegistration struct {
	observer BlueprintWriteObserver
	work     workscope.Group
}

// SetBlueprintWriteObserver installs write fencing and returns its owner's drain.
func SetBlueprintWriteObserver(o BlueprintWriteObserver) func(context.Context) error {
	registration := &blueprintRegistration{observer: o}
	blueprintWriteMu.Lock()
	blueprintWriteObserver = registration
	blueprintWriteMu.Unlock()
	return func(ctx context.Context) error {
		blueprintWriteMu.Lock()
		if blueprintWriteObserver == registration {
			blueprintWriteObserver = nil
		}
		blueprintWriteMu.Unlock()
		registration.work.Stop()
		if err := registration.work.Wait(ctx); err != nil {
			return err
		}
		blueprintWriteMu.Lock()
		registration.observer = nil
		blueprintWriteMu.Unlock()
		return nil
	}
}

func notifyBlueprintWrite(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	blueprintWriteMu.RLock()
	registration := blueprintWriteObserver
	if registration == nil || registration.observer == nil {
		blueprintWriteMu.RUnlock()
		return
	}
	workCtx, finish, err := registration.work.Begin(ctx)
	observer := registration.observer
	blueprintWriteMu.RUnlock()
	if err != nil {
		return
	}
	defer finish()
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		observer.AfterWrite(workCtx, sessionID, p)
	}
}
