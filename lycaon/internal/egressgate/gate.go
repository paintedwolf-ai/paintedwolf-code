// Package egressgate gates attributed outbound hosts before dialing.
package egressgate

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type contextKey struct{}

type State struct {
	Command confine.EgressCommand
	denied  atomic.Value
}

func WithAttribution(ctx context.Context, cmd confine.EgressCommand) context.Context {
	if strings.TrimSpace(cmd.SessionID) == "" {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, &State{Command: cmd})
}

func From(ctx context.Context) *State {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(contextKey{}).(*State)
	return state
}

type HostDeniedError struct{ Host string }

func (e *HostDeniedError) Error() string {
	if e == nil {
		return "host denied"
	}
	return fmt.Sprintf("host denied: %s", e.Host)
}

func AwaitHost(ctx context.Context, host string) error {
	return awaitEndpoint(ctx, host, host, "", "")
}

// AwaitHTTPRequest attributes one normalized HTTP hop, excluding its query.
func AwaitHTTPRequest(ctx context.Context, target *url.URL, method string) error {
	if target == nil {
		return nil
	}
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	id, label := secretmatch.HTTPDestination(target)
	recipient := secretmatch.Recipient{ID: id, Label: label, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}
	if secretcap.ResolutionFrom(ctx).RecipientApproved(recipient) {
		ctx = context.WithValue(ctx, consentContextKey{}, hopEndpoint(target))
	}
	return awaitEndpoint(ctx, target.Hostname(), hopEndpoint(target), method, path)
}

// hopEndpoint keeps services on different ports distinct.
func hopEndpoint(target *url.URL) string {
	host := strings.ToLower(strings.TrimSpace(target.Hostname()))
	if host == "" {
		return ""
	}
	port := target.Port()
	if port == "" {
		port = strconv.Itoa(int(outboundhttp.DefaultPort(target.Scheme)))
	}
	return net.JoinHostPort(host, port)
}

// Decisions use host and port; refusal messages identify the host.
func awaitEndpoint(ctx context.Context, host, endpoint, method, path string) error {
	state := From(ctx)
	if state == nil {
		return nil
	}
	host = strings.TrimSpace(host)
	endpoint = strings.TrimSpace(endpoint)
	if host == "" || endpoint == "" {
		return nil
	}
	reviewCtx := hitl.WaitContext(ctx)
	if consent, _ := ctx.Value(consentContextKey{}).(string); consent != "" {
		// The stop context drops invocation values; retain this hop's reviewed consent.
		reviewCtx = context.WithValue(reviewCtx, consentContextKey{}, consent)
	}
	if confine.DecideAttributedHTTPRequest(reviewCtx, state.Command, endpoint, method, path) {
		return nil
	}
	state.denied.Store(host)
	return &HostDeniedError{Host: host}
}

func IOContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	stop := hitl.WaitContext(ctx)
	if stop.Err() != nil {
		return stop, func() {}
	}
	return context.WithTimeout(stop, timeout)
}

func Denied(ctx context.Context) (string, bool) {
	state := From(ctx)
	if state == nil {
		return "", false
	}
	host, _ := state.denied.Load().(string)
	host = strings.TrimSpace(host)
	return host, host != ""
}

type consentContextKey struct{}

// RequestConsented binds a payload-screen approval to this HTTP hop's endpoint.
// Redirects acquire their own context and cannot inherit a different origin.
func RequestConsented(ctx context.Context, host string, port uint16) bool {
	endpoint, _ := ctx.Value(consentContextKey{}).(string)
	return endpoint != "" && endpoint == net.JoinHostPort(strings.ToLower(strings.TrimSpace(host)), fmt.Sprint(port))
}
