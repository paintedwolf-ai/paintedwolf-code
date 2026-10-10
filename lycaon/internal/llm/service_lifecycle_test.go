package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestServiceCloseWaitsForModelFeedRefresh(t *testing.T) {
	refreshCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(done)
		<-refreshCtx.Done()
		close(exited)
	}()

	service := &Service{
		modelFeedRefreshCancel: cancel,
		modelFeedRefreshDone:   done,
	}
	testutil.FailErr(t, "close service", service.Close(t.Context()))
	select {
	case <-exited:
	default:
		t.Fatal("model feed refresh still running")
	}
}

func TestCanceledServiceAllocationDoesNotCreateDeviceState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, filepath.Join(home, "device"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service, err := NewService(ctx, nil)
	if service != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled allocation = %v, %v", service, err)
	}
	entries, err := os.ReadDir(home)
	testutil.FailErr(t, "read untouched device directory", err)
	if len(entries) != 0 {
		t.Fatalf("canceled allocation created device state: %v", entries)
	}
}
