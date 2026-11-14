//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

var (
	parked   = make(chan struct{})
	hold     = make(chan struct{})
	parkOnce sync.Once
)

func prepareView(request adapter.PrepareRequest) (*fuselinux.Adapter, *adapter.PreparedView, func(*adapter.PreparedView), error) {
	adp := fuselinux.New(fuselinux.Config{
		MaxConcurrentRequests: 1,
		OnMutationHook: func(uint32, string) {
			parkOnce.Do(func() { close(parked) })
			<-hold
		},
	})
	view, err := adp.Prepare(context.Background(), request)
	return adp, view, parkThreadOnOwnMount, err
}

// parkThreadOnOwnMount parks a write in the single request worker, then an fsync, which FUSE waits on
// uninterruptibly, behind it. SIGKILL then leaves a thread waiting on a server that can never answer.
func parkThreadOnOwnMount(view *adapter.PreparedView) {
	file, err := os.Open(filepath.Join(view.PrimaryWorkPath, "f"))
	if err != nil {
		fail("open", err)
	}
	go func() { _ = os.WriteFile(filepath.Join(view.PrimaryWorkPath, "other"), []byte("x"), 0o644) }()
	<-parked
	go func() { _ = file.Sync() }()
}

func release(adp *fuselinux.Adapter, view *adapter.PreparedView) {
	close(hold)
	if _, err := adp.Release(context.Background(), view.ViewID); err != nil {
		fail("release", err)
	}
}
