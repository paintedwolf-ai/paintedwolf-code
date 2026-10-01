package llm

import (
	"context"
	"testing"

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
