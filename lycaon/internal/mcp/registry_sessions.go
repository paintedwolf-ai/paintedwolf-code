package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionRef identifies one provider connection within one scope.
type sessionRef struct {
	scope      string
	providerID string
}

type pooledSession struct {
	sess                  ProviderSession
	connectionFingerprint string
	// stop ends the session lifetime.
	stop context.CancelFunc
}

// ensureSession returns the pooled session for entry under scope, connecting if needed.
func (r *RegistryImpl) ensureSession(ctx context.Context, scope CallScope, entry MCPProviderEntry) (ProviderSession, error) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	ref := sessionRef{scope: scope.sessionKey(), providerID: entry.ID}
	entry = r.stampRecipeCredential(entry)
	wantFingerprint, err := providerConnectionFingerprint(entry)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	pooled, ok := r.sessions[ref]
	closed := r.closed
	r.mu.RUnlock()
	if ok && pooled.connectionFingerprint == wantFingerprint {
		return pooled.sess, nil
	}
	if ok {
		r.closeSessionRef(ref)
	}
	if closed {
		return nil, errors.New("mcp registry is closed")
	}

	if err := r.refreshHTTPAuth(ctx, entry); err != nil {
		return nil, err
	}

	// A local process shares the registry lifetime, while ctx bounds the handshake.
	lifetime, stop := context.WithCancel(r.lifeCtx)
	sess, err := r.connector.Connect(ctx, entry, ConnectOpts{
		ExtraEnv:          r.spawnEnv(entry),
		Roots:             r.rootsFor(scope),
		Lifetime:          lifetime,
		OnToolListChanged: r.toolListChangedNotifier(entry.ID),
		OnHTTPStatus:      r.httpStatusObserver(entry.ID),
	})
	if err != nil {
		stop()
		return nil, err
	}

	r.mu.Lock()
	if existing, raced := r.sessions[ref]; raced {
		// Keep the first concurrently published session.
		r.mu.Unlock()
		_ = sess.Close()
		stop()
		return existing.sess, nil
	}
	if r.closed {
		r.mu.Unlock()
		_ = sess.Close()
		stop()
		return nil, errors.New("mcp registry is closed")
	}
	r.sessions[ref] = &pooledSession{sess: sess, stop: stop, connectionFingerprint: wantFingerprint}
	delete(r.authRequired, entry.ID)
	r.mu.Unlock()
	return sess, nil
}

func providerConnectionFingerprint(entry MCPProviderEntry) (string, error) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("fingerprint mcp provider connection: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// rootsFor resolves project roots or device inspection roots.
func (r *RegistryImpl) rootsFor(scope CallScope) []string {
	if scope.isDevice() {
		return r.deviceProbeRoots()
	}
	return cleanRootPaths(scope.Roots)
}

// refreshHTTPAuth renews an OAuth access token before a connect that would otherwise
// present an expired one.
func (r *RegistryImpl) refreshHTTPAuth(ctx context.Context, entry MCPProviderEntry) error {
	tr, err := entry.Transport()
	if err != nil {
		return err
	}
	if tr != TransportHTTP {
		return nil
	}
	if entry.HasStaticHTTPAuth() {
		return nil
	}
	if !r.oauthStore.SignedIn(entry.ID) {
		return nil
	}
	if _, err := r.oauth.RefreshAccessToken(ctx, entry.ID); err != nil {
		// Only an authorization failure invalidates the stored grant.
		if !isOAuthAuthFailure(err) {
			return fmt.Errorf("mcp provider %q: token refresh failed: %w", entry.ID, err)
		}
		r.markAuthRequired(entry.ID)
		return fmt.Errorf("mcp provider %q: needs_auth: %w", entry.ID, err)
	}
	return nil
}

// httpStatusObserver records authorization failures from wire status codes.
func (r *RegistryImpl) httpStatusObserver(providerID string) func(int) {
	return func(code int) {
		switch code {
		case http.StatusUnauthorized, http.StatusForbidden:
			r.markAuthRequired(providerID)
		}
	}
}

func (r *RegistryImpl) markAuthRequired(providerID string) {
	r.mu.Lock()
	r.authRequired[providerID] = true
	r.mu.Unlock()
}

// toolListChangedNotifier hands the session a callback for
// notifications/tools/list_changed.
func (r *RegistryImpl) toolListChangedNotifier(providerID string) func() {
	return func() {
		select {
		case r.resync <- providerID:
		default:
			// A resync is already queued; the pending one will pick up this change too.
		}
	}
}

// drainResync handles tool-list changes outside the session reader.
func (r *RegistryImpl) drainResync() {
	for {
		select {
		case <-r.lifeCtx.Done():
			return
		case providerID := <-r.resync:
			slog.Info("mcp server reported a tool-list change; re-syncing",
				"provider_id", providerID)
			if err := r.SyncTools(r.lifeCtx); err != nil {
				slog.Warn("mcp resync after tool-list change failed",
					"provider_id", providerID, "error", err)
			}
		}
	}
}

func (r *RegistryImpl) spawnEnv(entry MCPProviderEntry) []string {
	if strings.TrimSpace(entry.Command) != DistroCommandSelf {
		return nil
	}
	r.mu.RLock()
	token := r.apiToken
	r.mu.RUnlock()
	if token == "" {
		return nil
	}
	return []string{"LYCAON_API_TOKEN=" + token}
}

// evictDeadSession drops a session closed by a known transport error, or every
// pooled session for a provider that just rejected its bearer with 401/403.
// httpStatusObserver has already marked authRequired during the failing round
// trip, so the next ensureSession re-authenticates rather than replaying the
// stale bearer. Eviction spans every scope because the bearer is provider-wide.
func (r *RegistryImpl) evictDeadSession(scope CallScope, providerID string, err error) {
	switch {
	case transportDead(err):
		slog.Warn("mcp session transport closed; evicting so the next call reconnects",
			"provider_id", providerID, "error", err)
		r.closeSessionRef(sessionRef{scope: scope.sessionKey(), providerID: providerID})
	case r.authRequiredNow(providerID):
		slog.Warn("mcp provider rejected its bearer with 401/403; evicting pooled sessions so the next call re-authenticates",
			"provider_id", providerID, "error", err)
		r.closeProviderSessions(providerID)
	}
}

// authRequiredNow reports whether providerID's most recent HTTP round trip
// came back 401/403 and the flag has not yet been cleared by a fresh connect.
func (r *RegistryImpl) authRequiredNow(providerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authRequired[providerID]
}

// transportDead distinguishes closed transports from declared tool errors.
func transportDead(err error) bool {
	if err == nil || toolrejection.AsToolReject(err) != nil {
		return false
	}
	return errors.Is(err, sdkmcp.ErrConnectionClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

// closeSessionRef tears down one pooled session.
func (r *RegistryImpl) closeSessionRef(ref sessionRef) {
	r.mu.Lock()
	pooled, ok := r.sessions[ref]
	delete(r.sessions, ref)
	r.mu.Unlock()
	if !ok || pooled == nil {
		return
	}
	if pooled.sess != nil {
		_ = pooled.sess.Close()
	}
	// Let graceful shutdown close the transport before cancellation.
	if pooled.stop != nil {
		pooled.stop()
	}
}

// closeProviderSessions tears down every scope's session for one provider.
func (r *RegistryImpl) closeProviderSessions(providerID string) {
	r.mu.RLock()
	refs := make([]sessionRef, 0)
	for ref := range r.sessions {
		if ref.providerID == providerID {
			refs = append(refs, ref)
		}
	}
	r.mu.RUnlock()
	for _, ref := range refs {
		r.closeSessionRef(ref)
	}
}

// CloseProjectSessions tears down every provider session for one project.
func (r *RegistryImpl) CloseProjectSessions(projectID string) {
	if r == nil {
		return
	}
	key := CallScope{ProjectID: projectID}.sessionKey()
	if key == deviceScopeKey {
		return
	}
	r.mu.RLock()
	refs := make([]sessionRef, 0)
	for ref := range r.sessions {
		if ref.scope == key {
			refs = append(refs, ref)
		}
	}
	r.mu.RUnlock()
	for _, ref := range refs {
		r.closeSessionRef(ref)
	}
}

// closeSessionsNotRunnable drops sessions absent from the runnable catalog.
func (r *RegistryImpl) closeSessionsNotRunnable(catalog []MergedMCPProviderEntry) {
	allowed := map[string]struct{}{}
	for _, s := range catalog {
		if s.Enabled {
			allowed[s.ID] = struct{}{}
		}
	}
	r.mu.RLock()
	refs := make([]sessionRef, 0)
	for ref := range r.sessions {
		if _, ok := allowed[ref.providerID]; !ok {
			refs = append(refs, ref)
		}
	}
	r.mu.RUnlock()
	for _, ref := range refs {
		r.closeSessionRef(ref)
	}
}
