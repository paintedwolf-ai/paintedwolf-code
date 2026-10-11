package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// oauthCallbackPath is the path the authorization server redirects back to.
const oauthCallbackPath = "/mcp/oauth/callback"

// oauthCallbackTimeout bounds how long a loopback listener waits for the browser.
// It is the same budget as the pending authorization it serves.
const oauthCallbackTimeout = pendingOAuthTTL

// callbackListener is a one-shot loopback HTTP server that receives an OAuth authorization code redirect.
type callbackListener struct {
	srv      *http.Server
	ln       net.Listener
	redirect string

	once sync.Once
	done chan struct{}
}

// callbackResult is what the browser handed back.
type callbackResult struct {
	Code  string
	State string
	// Err carries an authorization-server error response (RFC 6749 §4.1.2.1).
	Err error
}

// startCallbackListener binds a loopback listener and serves exactly one redirect.
//
// handle runs on the listener's goroutine with the parsed result; whatever it returns
// decides what the browser is shown. The listener stops after the first redirect it can
// parse, or when timeout elapses, whichever comes first.
func startCallbackListener(ctx context.Context, handle func(callbackResult) error) (*callbackListener, error) {
	// Explicitly IPv4 loopback: some authorization servers reject "localhost" and
	// registering both families would advertise a redirect we might not answer on.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth callback listener: %w", err)
	}
	l := &callbackListener{
		ln:       ln,
		done:     make(chan struct{}),
		redirect: fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, oauthCallbackPath),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(oauthCallbackPath, func(w http.ResponseWriter, req *http.Request) {
		res := parseCallbackRequest(req)
		if res.Code == "" && res.Err == nil {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}
		err := res.Err
		if err == nil {
			err = handle(res)
		}
		writeCallbackPage(w, err)
		l.finish(req.Context())
	})
	l.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = l.srv.Serve(ln) }()
	go func() {
		select {
		case <-l.done:
		case <-time.After(oauthCallbackTimeout):
			l.finish(ctx)
		}
	}()
	return l, nil
}

// RedirectURI is the loopback URL to register with the authorization server.
func (l *callbackListener) RedirectURI() string {
	if l == nil {
		return ""
	}
	return l.redirect
}

// finish shuts the listener down. Safe to call repeatedly and from either goroutine.
func (l *callbackListener) finish(parent context.Context) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.done)
		// Shutdown, not Close: the response to the browser is still being written by
		// the handler that called this, and Close would cut it off mid-flight.
		go func() {
			// Process-local teardown — not tied to any request ctx that may already be done.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer cancel()
			_ = l.srv.Shutdown(ctx)
		}()
	})
}

// parseCallbackRequest reads the redirect. Both query and form-encoded bodies are
// accepted: response_mode=form_post delivers the code in a POST body.
func parseCallbackRequest(req *http.Request) callbackResult {
	values := req.URL.Query()
	if req.Method == http.MethodPost {
		if err := req.ParseForm(); err == nil {
			for k, v := range req.PostForm {
				if len(v) > 0 {
					values.Set(k, v[0])
				}
			}
		}
	}
	res := callbackResult{
		Code:  strings.TrimSpace(values.Get("code")),
		State: strings.TrimSpace(values.Get("state")),
	}
	if code := strings.TrimSpace(values.Get("error")); code != "" {
		res.Err = &authorizationError{
			Code:        code,
			Description: strings.TrimSpace(values.Get("error_description")),
		}
	}
	return res
}

// authorizationError is an RFC 6749 §4.1.2.1 error response from the authorization
// endpoint. The code is a machine value from a closed set in that RFC.
type authorizationError struct {
	Code        string
	Description string
}

func (e *authorizationError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("authorization denied: %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("authorization denied: %s", e.Code)
}

// writeCallbackPage renders the page the user sees in their browser. It says only
// whether sign-in finished: any redirect chain can reach it, so it carries no server
// name, code, token, or action.
func writeCallbackPage(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	heading := "Signed in"
	detail := "You can close this window and return to the app."
	if err != nil {
		status = http.StatusBadRequest
		heading = "Sign-in failed"
		detail = "Return to the app and try again."
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, callbackPageHTML, heading, detail)
}

const callbackPageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%[1]s</title>
<style>
 body{font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
      display:grid;place-items:center;min-height:100vh;margin:0;color:#1c1c1e;background:#f5f5f7}
 main{text-align:center;padding:2rem}
 h1{font-size:1.25rem;margin:0 0 .5rem}
 p{margin:0;color:#6b6b70}
 @media (prefers-color-scheme:dark){body{color:#f5f5f7;background:#1c1c1e}p{color:#a1a1a6}}
</style></head>
<body><main><h1>%[1]s</h1><p>%[2]s</p></main></body></html>
`
