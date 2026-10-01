package observability

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// PerformanceHTTPMiddleware records body-free request latency and time to first
// byte. Streaming requests report their open lifetime separately from first byte.
func PerformanceHTTPMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op := StartPerformanceOperation("http.request", map[string]string{"method": r.Method})
			if op == nil {
				next.ServeHTTP(w, r)
				return
			}
			wrapped := &performanceResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(wrapped, r)
			pattern := "unmatched"
			if route := chi.RouteContext(r.Context()).RoutePattern(); strings.TrimSpace(route) != "" {
				pattern = route
			}
			op.SetDimension("route", pattern)
			op.SetDimension("status", strconv.Itoa(wrapped.status))
			if !wrapped.firstWrite.IsZero() {
				op.phases = append(op.phases, PerformancePhase{
					Name: "first_byte", DurationUS: wrapped.firstWrite.Sub(op.started).Microseconds(),
				})
			}
			outcome := "ok"
			switch {
			case r.Context().Err() != nil:
				outcome = "canceled"
			case wrapped.status >= http.StatusInternalServerError:
				outcome = "error"
			case wrapped.status >= http.StatusBadRequest:
				outcome = "rejected"
			}
			op.End(outcome)
		})
	}
}

type performanceResponseWriter struct {
	http.ResponseWriter
	status     int
	firstWrite time.Time
}

func (w *performanceResponseWriter) WriteHeader(status int) {
	if !w.firstWrite.IsZero() {
		return
	}
	w.firstWrite = time.Now()
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *performanceResponseWriter) Write(body []byte) (int, error) {
	if w.firstWrite.IsZero() {
		w.firstWrite = time.Now()
	}
	return w.ResponseWriter.Write(body)
}

func (w *performanceResponseWriter) Flush() {
	if w.firstWrite.IsZero() {
		w.firstWrite = time.Now()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *performanceResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *performanceResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *performanceResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

var _ interface {
	http.ResponseWriter
	http.Flusher
	http.Hijacker
	http.Pusher
	Unwrap() http.ResponseWriter
} = (*performanceResponseWriter)(nil)
