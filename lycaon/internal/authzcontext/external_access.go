package authzcontext

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// MaxExternalAccessSockets matches confine.MaxSocketGrants.
	MaxExternalAccessSockets = 8
	// MaxExternalAccessEndpoints bounds observed mediated endpoint rows on one detail object.
	MaxExternalAccessEndpoints = 64
)

// ExternalAccessInput collects machine facts for one bounded external-access object.
type ExternalAccessInput struct {
	Endpoints            []ExternalAccessEndpointInput
	Sockets              []ExternalAccessSocketInput
	DeclaredDestinations []string
	Direct               bool
	FullBypass           bool
	Detections           []api.ExternalAccessDetection
	// AffirmativeNone marks that the host established no exceptional access for this record.
	AffirmativeNone bool
	// Malformed forces visibility_summary unknown for incomplete detail recovery.
	Malformed bool
}

// ExternalAccessEndpointInput is one raw mediated observation before dedupe.
type ExternalAccessEndpointInput struct {
	Host      string
	Port      uint16
	Transport string
	Allowed   bool
	Attempts  int // when 0, counts as 1
}

// ExternalAccessSocketInput is one applied local-service socket grant.
type ExternalAccessSocketInput struct {
	ApprovedPath string
	ResolvedPath string
	Scope        string
}

// BuildExternalAccess assembles a redacted, capped, deterministically ordered object.
func BuildExternalAccess(in ExternalAccessInput) *api.ExternalAccess {
	if in.Malformed {
		return &api.ExternalAccess{
			Modes:             nil,
			VisibilitySummary: api.ExternalAccessVisibilitySummaryUnknown,
		}
	}

	endpoints := dedupeEndpoints(in.Endpoints)
	if len(endpoints) > MaxExternalAccessEndpoints {
		endpoints = endpoints[:MaxExternalAccessEndpoints]
	}
	sockets := buildSockets(in.Sockets)
	declared := redactDeclaredDestinations(in.DeclaredDestinations)
	detections := append([]api.ExternalAccessDetection(nil), in.Detections...)

	var direct *api.ExternalAccessDirect
	if in.Direct {
		direct = &api.ExternalAccessDirect{
			Scope:                       api.ExternalAccessDirectScopeCurrentAction,
			ActualDestinationVisibility: api.ExternalAccessVisibilityUnobserved,
		}
	}

	modes := deriveModes(endpoints, sockets, direct, in.FullBypass)
	ea := &api.ExternalAccess{
		Modes:                modes,
		Endpoints:            endpoints,
		Sockets:              sockets,
		DeclaredDestinations: declared,
		Direct:               direct,
		Detections:           detections,
	}
	ea.VisibilitySummary = api.ExternalAccessVisibilitySummary(SummarizeVisibility(ea, in.AffirmativeNone))
	return ea
}

// SummarizeVisibility returns the locked visibility summary for an ExternalAccess object.
// Pass affirmativeNone=true only when the host established no exceptional access.
func SummarizeVisibility(ea *api.ExternalAccess, affirmativeNone bool) string {
	if ea == nil {
		if affirmativeNone {
			return string(api.ExternalAccessVisibilitySummaryNone)
		}
		return string(api.ExternalAccessVisibilitySummaryUnknown)
	}

	hasObserved := false
	for _, ep := range ea.Endpoints {
		if ep.Visibility == api.ExternalAccessVisibilityObserved {
			hasObserved = true
			break
		}
	}
	hasUnobserved := ea.Direct != nil || len(ea.Sockets) > 0
	for _, mode := range ea.Modes {
		switch mode {
		case api.ExternalAccessModeFullBypass, api.ExternalAccessModeDirectIP, api.ExternalAccessModeLocalSocket:
			hasUnobserved = true
		case api.ExternalAccessModeMediatedHTTP, api.ExternalAccessModeMediatedSocks:
			hasObserved = true
		}
	}
	hasDeclared := len(ea.DeclaredDestinations) > 0

	switch {
	case hasObserved && hasUnobserved:
		return string(api.ExternalAccessVisibilitySummaryMixed)
	case hasObserved:
		return string(api.ExternalAccessVisibilitySummaryObserved)
	case hasUnobserved:
		return string(api.ExternalAccessVisibilitySummaryUnobserved)
	case hasDeclared:
		return string(api.ExternalAccessVisibilitySummaryDeclared)
	case affirmativeNone:
		return string(api.ExternalAccessVisibilitySummaryNone)
	default:
		return string(api.ExternalAccessVisibilitySummaryUnknown)
	}
}

func deriveModes(endpoints []api.ExternalAccessEndpoint, sockets []api.ExternalAccessSocket, direct *api.ExternalAccessDirect, fullBypass bool) []api.ExternalAccessMode {
	seen := map[api.ExternalAccessMode]bool{}
	var modes []api.ExternalAccessMode
	add := func(m api.ExternalAccessMode) {
		if seen[m] {
			return
		}
		seen[m] = true
		modes = append(modes, m)
	}
	for _, ep := range endpoints {
		switch ep.Transport {
		case api.ExternalAccessTransportSocksTCP:
			add(api.ExternalAccessModeMediatedSocks)
		default:
			add(api.ExternalAccessModeMediatedHTTP)
		}
	}
	if len(sockets) > 0 {
		add(api.ExternalAccessModeLocalSocket)
	}
	if direct != nil {
		add(api.ExternalAccessModeDirectIP)
	}
	if fullBypass {
		add(api.ExternalAccessModeFullBypass)
	}
	sort.Slice(modes, func(i, j int) bool { return modes[i] < modes[j] })
	return modes
}

func dedupeEndpoints(in []ExternalAccessEndpointInput) []api.ExternalAccessEndpoint {
	type key struct {
		host, transport, decision string
		port                      uint16
	}
	type agg struct {
		ep              api.ExternalAccessEndpoint
		allowed, denied int
		order           int
	}
	byKey := map[key]*agg{}
	order := 0
	for _, raw := range in {
		host := strings.TrimSpace(strings.ToLower(raw.Host))
		if host == "" {
			continue
		}
		transport := normalizeTransport(raw.Transport)
		decision := api.ExternalAccessDecisionDeny
		if raw.Allowed {
			decision = api.ExternalAccessDecisionAllow
		}
		k := key{host: host, port: raw.Port, transport: string(transport), decision: string(decision)}
		attempts := raw.Attempts
		if attempts <= 0 {
			attempts = 1
		}
		if cur, ok := byKey[k]; ok {
			cur.ep.AttemptCount += attempts
			if raw.Allowed {
				cur.allowed += attempts
			} else {
				cur.denied += attempts
			}
			continue
		}
		ep := api.ExternalAccessEndpoint{
			Host:         host,
			Port:         raw.Port,
			Transport:    transport,
			Visibility:   api.ExternalAccessVisibilityObserved,
			Decision:     decision,
			AttemptCount: attempts,
		}
		a := &agg{ep: ep, order: order}
		if raw.Allowed {
			a.allowed = attempts
		} else {
			a.denied = attempts
		}
		byKey[k] = a
		order++
	}
	list := make([]*agg, 0, len(byKey))
	for _, a := range byKey {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		return endpointLess(list[i].ep, list[j].ep)
	})
	out := make([]api.ExternalAccessEndpoint, 0, len(list))
	for _, a := range list {
		ep := a.ep
		if a.allowed > 0 {
			n := a.allowed
			ep.AllowedCount = &n
		}
		if a.denied > 0 {
			n := a.denied
			ep.DeniedCount = &n
		}
		out = append(out, ep)
	}
	return out
}

func endpointLess(a, b api.ExternalAccessEndpoint) bool {
	if a.Host != b.Host {
		return a.Host < b.Host
	}
	if a.Port != b.Port {
		return a.Port < b.Port
	}
	if a.Transport != b.Transport {
		return a.Transport < b.Transport
	}
	return a.Decision < b.Decision
}

func normalizeTransport(raw string) api.ExternalAccessTransport {
	switch strings.TrimSpace(raw) {
	case string(api.ExternalAccessTransportSocksTCP):
		return api.ExternalAccessTransportSocksTCP
	case string(api.ExternalAccessTransportHTTPRequest):
		return api.ExternalAccessTransportHTTPRequest
	default:
		return api.ExternalAccessTransportHTTPConnect
	}
}

func buildSockets(in []ExternalAccessSocketInput) []api.ExternalAccessSocket {
	byPair := map[string]api.ExternalAccessSocket{}
	for _, raw := range in {
		approved := redactPathForDetail(raw.ApprovedPath)
		resolved := redactPathForDetail(raw.ResolvedPath)
		if approved == "" && resolved == "" {
			continue
		}
		scope := strings.TrimSpace(raw.Scope)
		if scope == "" {
			scope = "current_action"
		}
		key := resolved + "\x00" + approved + "\x00" + scope
		byPair[key] = api.ExternalAccessSocket{
			ApprovedPath:          approved,
			ResolvedPath:          resolved,
			Scope:                 scope,
			CapabilityVisibility:  api.ExternalAccessVisibilityObserved,
			ConnectionVisibility:  api.ExternalAccessVisibilityUnobserved,
			InnerEffectVisibility: api.ExternalAccessVisibilityUnobserved,
			Authority:             api.ExternalAccessSocketAuthorityOutsideSandboxDaemon,
		}
	}
	out := make([]api.ExternalAccessSocket, 0, len(byPair))
	for _, s := range byPair {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResolvedPath != out[j].ResolvedPath {
			return out[i].ResolvedPath < out[j].ResolvedPath
		}
		if out[i].ApprovedPath != out[j].ApprovedPath {
			return out[i].ApprovedPath < out[j].ApprovedPath
		}
		return out[i].Scope < out[j].Scope
	})
	if len(out) > MaxExternalAccessSockets {
		out = out[:MaxExternalAccessSockets]
	}
	return out
}

func redactDeclaredDestinations(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := redactDeclaredDestination(raw)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func redactDeclaredDestination(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		q := u.Query()
		changed := false
		for key := range q {
			if queryKeyLooksSensitive(key) {
				q.Set(key, "REDACTED")
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		} else if u.RawQuery != "" {
			// Strip opaque query tokens when we cannot classify keys safely.
			u.RawQuery = ""
		}
		return u.String()
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func queryKeyLooksSensitive(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, frag := range []string{"token", "key", "secret", "password", "passwd", "pwd", "auth", "credential", "signature", "sig", "otp", "session", "sid"} {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

func redactPathForDetail(raw string) string {
	p := filepath.Clean(strings.TrimSpace(raw))
	if p == "" || p == "." {
		return ""
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		home = filepath.Clean(home)
		sep := string(filepath.Separator)
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+sep) {
			return "~/" + filepath.ToSlash(strings.TrimPrefix(p, home+sep))
		}
	}
	return filepath.ToSlash(p)
}
