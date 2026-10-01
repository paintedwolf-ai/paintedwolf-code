package app

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/httpio"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const profileAddrEnv = "LYCAON_PPROF_ADDR"

func (a *ServeApp) startProfileServer(ctx context.Context) error {
	addr := strings.TrimSpace(os.Getenv(profileAddrEnv))
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: %w", profileAddrEnv, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use a loopback IP address", profileAddrEnv)
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen for profiling on %s: %w", addr, err)
	}
	server := &http.Server{
		Handler:           authenticatedProfileHandler(a.APIToken),
		ReadHeaderTimeout: 5 * time.Second,
	}
	a.resources.setProfileServer(server, listener)
	wg := &a.profileWG
	wg.Add(1)
	go func() {
		defer wg.Done()
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.ErrorContext(ctx, "profile server stopped", "error", serveErr)
		}
	}()
	slog.InfoContext(ctx, "profile server listening", "addr", listener.Addr().String())
	return nil
}

func authenticatedProfileHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := api.BearerTokenFromRequest(r)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpio.WriteJSON(w, http.StatusUnauthorized, wire.ErrorResponse{
				Code:    wire.ApiErrorCodeUnauthorized,
				Message: "The request is not authorized.",
			})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
