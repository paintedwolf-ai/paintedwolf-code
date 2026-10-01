package httpclient

import (
	"context"
	"net/http/httptrace"
	"sync"
)

// RequestObservation records transport milestones for one HTTP attempt.
type RequestObservation struct {
	// GotConn reports a dialed or pooled connection.
	GotConn bool
	// WroteRequest reports a complete request write.
	WroteRequest bool
	// FirstByte reports that the response began arriving.
	FirstByte bool
}

// requestObserver serializes transport callbacks.
type requestObserver struct {
	mu  sync.Mutex
	obs RequestObservation
}

// Observe records transport progress for one attempt.
func Observe(ctx context.Context) (context.Context, func() RequestObservation) {
	o := &requestObserver{}
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			o.mu.Lock()
			o.obs.GotConn = true
			o.mu.Unlock()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			// A partial write is not a delivered request.
			if info.Err != nil {
				return
			}
			o.mu.Lock()
			o.obs.WroteRequest = true
			o.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			o.mu.Lock()
			o.obs.FirstByte = true
			o.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace), func() RequestObservation {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.obs
	}
}

// Delivered reports a complete request write.
func (o RequestObservation) Delivered() bool { return o.WroteRequest }
