//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

func prepareView(request adapter.PrepareRequest) (*nfsdarwin.Adapter, *adapter.PreparedView, func(*adapter.PreparedView), error) {
	request.QuiesceHook, request.FlushClientHook = nfsdarwin.NoopQuiesceHook, nfsdarwin.NoWritableMappingsFlushHook
	adp := nfsdarwin.New(nfsdarwin.Config{})
	view, err := adp.Prepare(context.Background(), request)
	return adp, view, keepThreadsInOwnMount, err
}

// keepThreadsInOwnMount leaves requests in flight, so a kill finds threads waiting on this server.
func keepThreadsInOwnMount(view *adapter.PreparedView) {
	path := filepath.Join(view.PrimaryWorkPath, "f")
	for range 4 {
		go func() {
			for {
				_, _ = os.ReadFile(path)
			}
		}()
	}
}

func release(adp *nfsdarwin.Adapter, view *adapter.PreparedView) {
	if _, err := adp.Release(context.Background(), view.ViewID); err != nil {
		fail("release", err)
	}
}
