package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

// resolverRecordingGuard observes the page-target resolver the manager installs.
type resolverRecordingGuard struct {
	*loopguard.MemoryDoomLoopGuard
	resolver func(sessionID, pageID string) string
}

func (g *resolverRecordingGuard) SetPageTargetResolver(fn func(sessionID, pageID string) string) {
	g.resolver = fn
	g.MemoryDoomLoopGuard.SetPageTargetResolver(fn)
}

func newResolverRecordingGuard() *resolverRecordingGuard {
	return &resolverRecordingGuard{MemoryDoomLoopGuard: loopguard.NewMemoryDoomLoopGuard()}
}

func TestDoomLoopPageResolverWiringOrder(t *testing.T) {
	for _, guardFirst := range []bool{true, false} {
		name := "pages first"
		if guardFirst {
			name = "guard first"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewManager(sessionstore.NewMemory(), nil, nil, settings.DefaultSessionLimits())
			guard := newResolverRecordingGuard()
			pages := pagesession.NewRegistry(pagesession.DefaultConfig())
			t.Cleanup(func() { pages.Close(t.Context()) })
			if guardFirst {
				manager.SetDoomLoopGuard(guard)
				manager.SetPageRegistry(pages)
			} else {
				manager.SetPageRegistry(pages)
				manager.SetDoomLoopGuard(guard)
			}
			if guard.resolver == nil {
				t.Fatal("page target resolver was not installed")
			}
			replacement := newResolverRecordingGuard()
			manager.SetDoomLoopGuard(replacement)
			if replacement.resolver == nil {
				t.Fatal("replacement guard lost page target resolution")
			}
		})
	}
}
