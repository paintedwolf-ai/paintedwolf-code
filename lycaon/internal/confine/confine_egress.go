package confine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// SSHKnownHostsPath returns the project's known_hosts scratch path. Keying it by
// project keeps the value a command reads the same on every invocation; keys
// accepted here are already inside the project's trust domain.
func SSHKnownHostsPath(c Confinement) string {
	project := strings.TrimSpace(c.ProjectID)
	if project == "" {
		return ""
	}
	return filepath.Join(os.TempDir(), "lycaon-ssh-known-"+project)
}

// EgressCommand attributes a broker connection to its command.
type EgressCommand struct {
	SessionID     string
	RootSessionID string // coordinator chat id when SessionID is a worker; empty ⇒ SessionID is the chat
	ProjectID     string
	ProjectDir    string
	ToolCallID    string
	// ToolName is the argv-running tool associated with this token (command, verify, …).
	ToolName    string
	Image       string // semantic tool image when there is no command line
	CommandLine string // exact command attributed to this execution
	// DeclaredHosts is the host-derived destination set for this action.
	DeclaredHosts []string
	// ReducedPackageExecution restricts egress to reviewed registries.
	ReducedPackageExecution bool
	// UserRule is the matching ask-rule citation.
	UserRule *EgressUserRule
}

// EgressUserRule is a redaction-safe rule citation.
type EgressUserRule struct {
	Category string
	Pattern  string
	Subject  string
	UnitID   string
	PackID   string
	Scope    string
}

// NormalizeDeclaredHosts returns a stable declared destination set.
func NormalizeDeclaredHosts(hosts []string) []string {
	if len(hosts) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// DeclaresHost reports whether host is part of this action's declared destination set.
func (c EgressCommand) DeclaresHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(c.DeclaredHosts) == 0 {
		return false
	}
	for _, h := range c.DeclaredHosts {
		if h == host {
			return true
		}
	}
	return false
}

// DeclaredHostsDigest identifies a declared destination set without exposing it.
func DeclaredHostsDigest(hosts []string) (string, int) {
	normalized := NormalizeDeclaredHosts(hosts)
	if len(normalized) == 0 {
		return "", 0
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:]), len(normalized)
}

// EgressHost is one destination a confined command reached (or was blocked from).
type EgressHost struct {
	Host           string
	Port           uint16 `json:",omitempty"`
	Transport      string `json:",omitempty"`
	RequestMethod  string `json:",omitempty"`
	RequestPath    string `json:",omitempty"`
	ResponseStatus int    `json:",omitempty"`
	Outcome        string `json:",omitempty"`
	Allowed        bool
	// Inherited marks a destination reached by a process an earlier command left
	// running. This action answered for it; it did not make the request.
	Inherited bool `json:",omitempty"`
	Attempts  int
	// DialError is the latest upstream failure after policy allowed the dial.
	DialError string `json:",omitempty"`
}

// EgressDetectionCitation identifies the rule that held a connection.
type EgressDetectionCitation struct {
	// Tagged distinguishes absent effect metadata from false values.
	External      bool
	Local         bool
	Unrecoverable bool
	Tagged        bool
	PackID        string
	RuleID        string
	RuleTitle     string
	Level         string
	CorrelationID string
}

// EgressDetectionObservation is the broker-normalized connection attempt passed
// to an optional detection overlay before dialing.
type EgressDetectionObservation struct {
	Endpoint      egressproxy.Endpoint
	Image         string
	CommandLine   string
	SessionID     string
	ActionID      string
	Origin        string
	DecisionStage string
}

// EgressDetectionSource runs after host rules and before automatic allows.
type EgressDetectionSource interface {
	Match(observation EgressDetectionObservation) (EgressDetectionCitation, bool)
	// Escalates uses the approval posture.
	Escalates(match EgressDetectionCitation, approvalPosture gate.Posture) bool
}

// EgressResolver returns the approval decision for one observed destination.
type EgressResolver func(ctx context.Context, cmd EgressCommand, ep egressproxy.Endpoint, detection *EgressDetectionCitation) bool

// EgressPosture is the default when no host rule matches.
type EgressPosture int

const (
	// PostureObserve records and allows unmatched hosts.
	PostureObserve EgressPosture = iota
	// PostureAsk raises the approval card the first time a command reaches a new host.
	PostureAsk
)

var (
	postureMu   sync.RWMutex
	currentPstr = PostureObserve
)

// SetEgressPosture sets the default network posture.
func SetEgressPosture(p EgressPosture) {
	postureMu.Lock()
	currentPstr = p
	postureMu.Unlock()
}

func currentPosture() EgressPosture {
	postureMu.RLock()
	defer postureMu.RUnlock()
	return currentPstr
}

// PostureString is the wire token for a posture.
func PostureString(p EgressPosture) string {
	if p == PostureAsk {
		return "ask"
	}
	return "observe"
}

// egressBrokerT coordinates per-action network decisions.
type egressBrokerT struct {
	mu       sync.Mutex
	verdict  scopedstore.LRU[bool]    // exact action + approval subject key → allow
	waiters  map[string][]chan bool   // keys currently parked on the resolver
	tokens   map[string]EgressCommand // action lineage → identity
	egress   map[string][]EgressHost  // action lineage → bounded endpoint decisions
	loopback map[string]func(uint16) bool
	// lineages retains an action's identity past its lease so a process it left
	// running can still be placed against its project.
	lineages     map[string]EgressCommand
	lineageSeq   map[string]uint64
	lineageAge   []string
	lineageOrder uint64
	resolver     EgressResolver
}

// retainedLineages bounds how many ended actions stay placeable.
const retainedLineages = 256

var egressBroker = &egressBrokerT{
	waiters:    map[string][]chan bool{},
	tokens:     map[string]EgressCommand{},
	egress:     map[string][]EgressHost{},
	loopback:   map[string]func(uint16) bool{},
	lineages:   map[string]EgressCommand{},
	lineageSeq: map[string]uint64{},
}

var (
	approvalsDisabledMu  sync.RWMutex
	approvalsDisabledSrc func(EgressCommand) bool
)

// SetApprovalsDisabledSource sets the live discretionary-prompt switch.
func SetApprovalsDisabledSource(fn func(EgressCommand) bool) {
	approvalsDisabledMu.Lock()
	approvalsDisabledSrc = fn
	approvalsDisabledMu.Unlock()
}

func approvalsDisabled(cmd EgressCommand) bool {
	approvalsDisabledMu.RLock()
	fn := approvalsDisabledSrc
	approvalsDisabledMu.RUnlock()
	return fn != nil && fn(cmd)
}

var (
	egressPostureResolverMu sync.RWMutex
	egressPostureResolver   func(EgressCommand) EgressPosture
)

// SetEgressPostureResolver installs the per-action posture reader.
func SetEgressPostureResolver(fn func(EgressCommand) EgressPosture) {
	egressPostureResolverMu.Lock()
	egressPostureResolver = fn
	egressPostureResolverMu.Unlock()
}

// EffectivePosture resolves the per-action network posture.
func EffectivePosture(cmd EgressCommand) EgressPosture {
	egressPostureResolverMu.RLock()
	fn := egressPostureResolver
	egressPostureResolverMu.RUnlock()
	if fn != nil {
		return fn(cmd)
	}
	return currentPosture()
}

// SetEgressResolver installs the approval hook for unmatched hosts.
func SetEgressResolver(fn EgressResolver) {
	egressBroker.mu.Lock()
	egressBroker.resolver = fn
	egressBroker.mu.Unlock()
}

var (
	egressDetectionMu      sync.RWMutex
	egressDetectionSrc     EgressDetectionSource
	detectionPermPostureFn func(EgressCommand) gate.Posture
)

// SetEgressDetectionSource installs the detection-pack CONNECT overlay. Nil disables it.
func SetEgressDetectionSource(src EgressDetectionSource) {
	egressDetectionMu.Lock()
	egressDetectionSrc = src
	egressDetectionMu.Unlock()
}

// SetDetectionApprovalPosture installs the detection severity posture reader.
func SetDetectionApprovalPosture(fn func(EgressCommand) gate.Posture) {
	egressDetectionMu.Lock()
	detectionPermPostureFn = fn
	egressDetectionMu.Unlock()
}

func currentEgressDetection(cmd EgressCommand) (EgressDetectionSource, gate.Posture) {
	egressDetectionMu.RLock()
	defer egressDetectionMu.RUnlock()
	var posture gate.Posture
	if detectionPermPostureFn != nil {
		posture = detectionPermPostureFn(cmd)
	}
	return egressDetectionSrc, posture
}

// loopbackConnectAuthority projects the boundary's local port grant.
func loopbackConnectAuthority(c *Confinement) func(uint16) bool {
	if c == nil || !c.LoopbackConnect {
		return nil
	}
	ports := append([]uint16(nil), c.LoopbackConnectPorts...)
	return func(port uint16) bool {
		// An unnarrowed grant is every local port, as writeLoopbackConnectRules emits.
		if len(ports) == 0 {
			return true
		}
		for _, granted := range ports {
			if granted == port {
				return true
			}
		}
		return false
	}
}

// ForgetEgressAction expires every verdict for one completed action.
// Ordinary session+host decisions are retained.
func ForgetEgressAction(sessionID, actionID string) {
	egressBroker.mu.Lock()
	egressBroker.forgetActionLocked(sessionID, actionID)
	egressBroker.mu.Unlock()
}

func (b *egressBrokerT) forgetActionLocked(sessionID, actionID string) {
	sessionID = strings.TrimSpace(sessionID)
	actionID = strings.TrimSpace(actionID)
	if sessionID == "" || actionID == "" {
		return
	}
	prefixes := []string{
		"host\x00" + sessionID + "\x00" + actionID + "\x00",
		"socks\x00" + sessionID + "\x00" + actionID + "\x00",
		"declared\x00" + sessionID + "\x00" + actionID + "\x00",
		"detection\x00" + sessionID + "\x00" + actionID + "\x00",
	}
	for key := range b.verdict.Snapshot() {
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				b.verdict.Delete(key)
				break
			}
		}
	}
}

// ForgetEgressSession releases completed decisions for a session.
func ForgetEgressSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	egressBroker.mu.Lock()
	defer egressBroker.mu.Unlock()
	for key := range egressBroker.verdict.Snapshot() {
		if strings.HasPrefix(key, "host\x00"+sessionID+"\x00") ||
			strings.HasPrefix(key, "socks\x00"+sessionID+"\x00") ||
			strings.HasPrefix(key, "declared\x00"+sessionID+"\x00") ||
			strings.HasPrefix(key, "detection\x00"+sessionID+"\x00") {
			egressBroker.verdict.Delete(key)
		}
	}
}

func (b *egressBrokerT) observeEndpoint(token string, ep egressproxy.Endpoint, allowed bool) {
	host := ep.Host
	if token == "" || host == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	command, attributed := b.tokens[token]
	if !attributed {
		return
	}
	for i := range b.egress[token] {
		h := &b.egress[token][i]
		if sameEgressEndpoint(*h, ep) && h.Allowed == allowed {
			h.Attempts++
			return
		}
	}
	obs := EgressHost{
		Host:          host,
		Port:          ep.Port,
		Transport:     string(ep.Transport),
		RequestMethod: ep.RequestMethod,
		RequestPath:   ep.RequestPath,
		Allowed:       allowed,
		Inherited:     ep.Inherited,
		Attempts:      1,
	}
	if len(b.egress[token]) >= maxEgressEndpointsPerCommand {
		if allowed {
			return
		}
		packageDenied := command.ReducedPackageExecution && !command.DeclaresHost(host)
		for i, retained := range b.egress[token] {
			// Preserve the strongest observed refusal.
			if retained.Allowed || (packageDenied && command.DeclaresHost(retained.Host)) {
				b.egress[token][i] = obs
				return
			}
		}
		return
	}
	b.egress[token] = append(b.egress[token], obs)
}

func sameEgressEndpoint(observed EgressHost, ep egressproxy.Endpoint) bool {
	return observed.Host == ep.Host && observed.Port == ep.Port && observed.Transport == string(ep.Transport) &&
		observed.RequestMethod == ep.RequestMethod && observed.RequestPath == ep.RequestPath &&
		observed.Inherited == ep.Inherited
}

// Denial evidence takes precedence when endpoint detail reaches this bound.
const maxEgressEndpointsPerCommand = 64

// reportDialOutcome records the upstream connection result.
func (b *egressBrokerT) reportDialOutcome(token string, ep egressproxy.Endpoint, dialErr error) {
	if token == "" || ep.Host == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, attributed := b.tokens[token]; !attributed {
		return
	}
	for i := range b.egress[token] {
		h := &b.egress[token][i]
		if !sameEgressEndpoint(*h, ep) || !h.Allowed {
			continue
		}
		if dialErr == nil {
			h.DialError = ""
			h.Outcome = "connected"
		} else {
			h.DialError = dialErrorLabel(dialErr)
			h.Outcome = "connect_failed"
		}
		return
	}
}

func (b *egressBrokerT) reportHTTPOutcome(token string, ep egressproxy.Endpoint, statusCode int, requestErr error) {
	if token == "" || ep.Host == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, attributed := b.tokens[token]; !attributed {
		return
	}
	for i := range b.egress[token] {
		h := &b.egress[token][i]
		if !sameEgressEndpoint(*h, ep) || !h.Allowed {
			continue
		}
		h.ResponseStatus = statusCode
		if requestErr == nil {
			h.Outcome = "response_received"
		} else {
			h.Outcome = "request_failed"
		}
		return
	}
}

// dialErrorLabel names a dial failure from its typed error; an unrecognized
// error keeps its message, truncated.
func dialErrorLabel(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return "no route to host"
	}
	if errors.Is(err, syscall.ETIMEDOUT) {
		return "timeout"
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return "permission denied"
	}
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

func (b *egressBrokerT) decideEndpoint(ctx context.Context, token string, ep egressproxy.Endpoint) bool {
	allow := b.decideVerdictEndpoint(ctx, token, ep)
	b.observeEndpoint(token, ep, allow)
	return allow
}

// DecideAttributedHost gates an attributed host without request metadata.
func DecideAttributedHost(ctx context.Context, cmd EgressCommand, host string) bool {
	return DecideAttributedHTTPRequest(ctx, cmd, host, "", "")
}

// DecideAttributedHTTPRequest gates an attributed request with pack metadata.
func DecideAttributedHTTPRequest(ctx context.Context, cmd EgressCommand, host, method, path string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.TrimSpace(cmd.SessionID) == "" {
		return true
	}
	ep, err := egressproxy.ParseHTTPEndpoint(host, egressproxy.TransportHTTPRequest)
	if err != nil {
		return false
	}
	ep.RequestMethod = strings.ToUpper(strings.TrimSpace(method))
	ep.RequestPath = strings.TrimSpace(path)
	return egressBroker.decideAttributedEndpoint(ctx, cmd, ep)
}

func (b *egressBrokerT) decideAttributedEndpoint(ctx context.Context, cmd EgressCommand, ep egressproxy.Endpoint) bool {
	allow, decided, rule, det := b.ruleOrPosture(ctx, cmd, ep, "attributed_host")
	if decided {
		return allow
	}
	cmd.UserRule = rule
	return b.askOrCache(ctx, cmd, true, cmd.ToolCallID, ep, det)
}

func (b *egressBrokerT) decideVerdictEndpoint(ctx context.Context, token string, ep egressproxy.Endpoint) bool {
	b.mu.Lock()
	cmd, attributed := b.tokens[token]
	b.mu.Unlock()
	allow, decided, rule, det := b.ruleOrPosture(ctx, cmd, ep, "confined_proxy")
	if decided {
		return allow
	}
	cmd.UserRule = rule
	return b.askOrCache(ctx, cmd, attributed, token, ep, det)
}

// ruleOrPosture applies host-rule enforcement and prompt posture.
func (b *egressBrokerT) ruleOrPosture(ctx context.Context, cmd EgressCommand, ep egressproxy.Endpoint, origin string) (allow, decided bool, rule *EgressUserRule, detection *EgressDetectionCitation) {
	// Authored denials take precedence over reusable approvals.
	if eval := currentRuleEval(); eval != nil {
		matched := eval(ctx, cmd, ep.Host)
		switch matched.Effect {
		case EgressRuleDeny:
			return false, true, nil, nil
		case EgressRuleAsk:
			rule = &EgressUserRule{
				Category: "host", Pattern: matched.Pattern, Subject: ep.Host,
				UnitID: matched.UnitID, PackID: matched.PackID, Scope: matched.Scope,
			}
		}
	}
	posture := EffectivePosture(cmd)
	// Package execution denies undeclared destinations.
	if cmd.ReducedPackageExecution {
		return cmd.DeclaresHost(ep.Host), true, nil, nil
	}
	// Disabled approvals bypass the remaining ask-only checks.
	if approvalsDisabled(cmd) {
		return true, true, nil, nil
	}
	// Escalating detection matches become gate inputs.
	if src, posture := currentEgressDetection(cmd); src != nil {
		observation := EgressDetectionObservation{
			Endpoint: ep, Image: cmd.Image, CommandLine: cmd.CommandLine,
			SessionID: cmd.SessionID, ActionID: cmd.ToolCallID,
			Origin: origin, DecisionStage: "pre_dial",
		}
		if m, ok := src.Match(observation); ok && src.Escalates(m, posture) {
			cit := m
			return false, false, rule, &cit
		}
	}
	// Undeclared destinations from untrusted content become gate inputs.
	if !retrievalDial(cmd) && !cmd.DeclaresHost(ep.Host) && sessionIngestedUntrusted(ctx, cmd) {
		return false, false, rule, nil
	}
	// Explicit rules and Ask posture feed the gate after enforcement checks.
	if rule != nil || (posture == PostureAsk && !cmd.DeclaresHost(ep.Host)) {
		return false, false, rule, nil
	}
	return true, true, nil, nil
}

// askOrCache coalesces and caches a verdict within one action.
func (b *egressBrokerT) askOrCache(ctx context.Context, cmd EgressCommand, attributed bool, actionID string, ep egressproxy.Endpoint, detection *EgressDetectionCitation) bool {
	b.mu.Lock()
	if !attributed {
		b.mu.Unlock()
		// Ask posture denies dials without session attribution.
		return EffectivePosture(cmd) != PostureAsk
	}
	key := egressVerdictKey(cmd, actionID, ep, detection)
	if key == "" {
		resolver := b.resolver
		b.mu.Unlock()
		if resolver == nil {
			return false
		}
		return resolver(ctx, cmd, ep, detection)
	}
	if v, ok := b.verdict.Load(key); ok {
		b.mu.Unlock()
		return v
	}
	if chs, parked := b.waiters[key]; parked {
		ch := make(chan bool, 1)
		b.waiters[key] = append(chs, ch)
		b.mu.Unlock()
		select {
		case v := <-ch:
			return v
		case <-ctx.Done():
			return false
		}
	}
	resolver := b.resolver
	if resolver == nil {
		b.verdict.Store(key, false)
		b.mu.Unlock()
		return false
	}
	b.waiters[key] = nil
	b.mu.Unlock()

	allow := resolver(ctx, cmd, ep, detection)
	b.mu.Lock()
	b.verdict.Store(key, allow)
	parked := b.waiters[key]
	delete(b.waiters, key)
	b.mu.Unlock()
	for _, ch := range parked {
		ch <- allow
	}
	return allow
}

func egressVerdictKey(cmd EgressCommand, actionID string, ep egressproxy.Endpoint, detection *EgressDetectionCitation) string {
	host := ep.Host
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return ""
	}
	// One declared-set verdict releases its sibling endpoints.
	if cmd.DeclaresHost(ep.Host) && len(cmd.DeclaredHosts) > 0 {
		digest, count := DeclaredHostsDigest(cmd.DeclaredHosts)
		if digest == "" {
			return ""
		}
		return fmt.Sprintf("declared\x00%s\x00%s\x00%s\x00%d", cmd.SessionID, actionID, digest, count)
	}
	// Endpoint verdict identity includes port and transport.
	if detection != nil {
		return fmt.Sprintf("detection\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
			cmd.SessionID, actionID, host, ep.Port, ep.Transport,
			detection.PackID, detection.RuleID, detection.Level)
	}
	return fmt.Sprintf("endpoint\x00%s\x00%s\x00%s\x00%d\x00%s",
		cmd.SessionID, actionID, host, ep.Port, ep.Transport)
}

var (
	egressDegradedMu sync.RWMutex
	egressDegraded   *EgressDegraded
)

// EgressDegraded is the read-only app/session status fact when mediation cannot start.
type EgressDegraded struct {
	Reason string // stable machine reason; never includes tokens or socket paths
}

// EgressDegradedState reports whether mediation is unavailable and why.
func EgressDegradedState() (EgressDegraded, bool) {
	egressDegradedMu.RLock()
	defer egressDegradedMu.RUnlock()
	if egressDegraded == nil {
		return EgressDegraded{}, false
	}
	return *egressDegraded, true
}

func recordEgressDegraded(reason string, err error) {
	egressDegradedMu.Lock()
	if egressDegraded == nil {
		egressDegraded = &EgressDegraded{Reason: reason}
		slogWarnBrokerFailure(reason, err)
	}
	egressDegradedMu.Unlock()
}

func slogWarnBrokerFailure(reason string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	logBrokerStartFailure(reason, msg)
}

// runtimeProxyOptIn enables proxy variables for runtimes that require it.
var runtimeProxyOptIn = []string{
	"NODE_USE_ENV_PROXY=1",
}

// ProxyEnv returns the enabled proxy variables for a confined command.
//
// The value carries no credential and no per-action address, so two invocations
// of one command under one boundary get byte-identical environments. Identity is
// the caller's lineage, which the broker reads from the connection.
func ProxyEnv(c Confinement) []string {
	if c.ProxyAddr == "" {
		return nil
	}
	httpU := "http://" + c.ProxyAddr
	env := []string{
		"HTTP_PROXY=" + httpU,
		"HTTPS_PROXY=" + httpU,
		"http_proxy=" + httpU,
		"https_proxy=" + httpU,
	}
	if c.SocksProxyEnv && c.SocksProxyAddr != "" {
		socksU := "socks5h://" + c.SocksProxyAddr
		env = append(env, "ALL_PROXY="+socksU, "all_proxy="+socksU)
	}
	// Local HTTP remains mediated by the action lease.
	env = append(env, "NO_PROXY=", "no_proxy=")
	return append(env, runtimeProxyOptIn...)
}

// proxyEnvKeys prevent inherited proxy settings from bypassing the mode.
var proxyEnvKeys = buildProxyEnvKeys()

// ProxyEnvKeys returns the managed proxy variable names.
func ProxyEnvKeys() []string {
	out := make([]string, 0, len(proxyEnvKeys))
	for key := range proxyEnvKeys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func buildProxyEnvKeys() map[string]struct{} {
	keys := map[string]struct{}{
		"HTTP_PROXY":  {},
		"HTTPS_PROXY": {},
		"ALL_PROXY":   {},
		"NO_PROXY":    {},
		"http_proxy":  {},
		"https_proxy": {},
		"all_proxy":   {},
		"no_proxy":    {},
	}
	for _, entry := range runtimeProxyOptIn {
		if key, _, ok := strings.Cut(entry, "="); ok {
			keys[key] = struct{}{}
		}
	}
	return keys
}

// WithoutProxyEnv removes ambient proxy routes.
func WithoutProxyEnv(base []string) []string {
	if len(base) == 0 {
		return nil
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, blocked := proxyEnvKeys[key]; blocked {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// OverlayEnv replaces matching base entries because getenv uses the first match.
func OverlayEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	keys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			keys[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := keys[key]; replaced {
			continue
		}
		out = append(out, entry)
	}
	return append(out, overrides...)
}

// ProcessEnvironment constructs the network-relevant child environment.
func ProcessEnvironment(base []string, c Confinement) []string {
	clean := WithoutProxyEnv(base)
	if c.Network != NetworkProxyOnly {
		return clean
	}
	return OverlayEnv(clean, ProxyEnv(c))
}

// GitSSHEnv returns mediated SSH environment variables.
func GitSSHEnv(c Confinement, selfPath, knownHosts string) []string {
	if c.Network != NetworkProxyOnly || c.SocksProxyAddr == "" {
		return nil
	}
	selfPath = strings.TrimSpace(selfPath)
	knownHosts = strings.TrimSpace(knownHosts)
	if selfPath == "" || knownHosts == "" {
		return nil
	}
	// Each shell layer receives its own quoting.
	proxyCmd := shellQuote(selfPath) + " ssh-proxy-command %h %p"
	// The host mirror supplies read-only key verification.
	knownHostsFiles := knownHosts
	if mirror := hostKnownHostsMirror(); mirror != "" {
		knownHostsFiles = knownHosts + " " + mirror
	}
	sshCmd := strings.Join([]string{
		"ssh",
		"-o", shellQuote("StrictHostKeyChecking=accept-new"),
		"-o", shellQuote("UserKnownHostsFile=" + knownHostsFiles),
		"-o", shellQuote("GlobalKnownHostsFile=/dev/null"),
		"-o", shellQuote("ProxyCommand=" + proxyCmd),
	}, " ")
	return []string{
		"GIT_SSH_COMMAND=" + sshCmd,
		"LYCAON_SOCKS_PROXY=" + c.SocksProxyAddr,
		"LYCAON_SSH_KNOWN_HOSTS=" + knownHosts,
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
