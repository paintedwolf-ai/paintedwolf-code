package watch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

const (
	MaxConcurrentProbes   = 4
	MaxSamplerSubscribers = 256
	SamplerMinInterval    = 250 * time.Millisecond
	SamplerMaxInterval    = 2 * time.Second
)

// ProbeKey isolates sampled probes by authority domain, kind, and normalized target.
type ProbeKey struct {
	AuthorityDomain string
	Kind            string
	Target          string
}

type probeOutcome struct {
	Known     bool
	Satisfied bool
	Denied    bool
	Code      string
	Status    int
}

type probeSubscriber struct {
	watchID string
	altIdx  int
}

type probeGroup struct {
	id          string
	key         ProbeKey
	condition   awaitstore.Condition
	authority   Authority
	owner       Owner
	toolCallID  string
	interval    time.Duration
	subscribers map[string]probeSubscriber
	probeCount  int64
}

// Sampler schedules bounded, fair, deduplicated readiness probes on the shared deadline heap.
type Sampler struct {
	mu         sync.Mutex
	groupsByID map[string]*probeGroup
	watchToIDs map[string][]string
	rrQueue    []string
	queued     map[string]bool
	totalRuns  int64
}

func newSampler() *Sampler {
	return &Sampler{
		groupsByID: make(map[string]*probeGroup),
		watchToIDs: make(map[string][]string),
		queued:     make(map[string]bool),
	}
}

func buildAuthorityDomain(owner Owner, auth Authority) string {
	ports := append([]uint16(nil), auth.LoopbackPorts...)
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	var portStrs []string
	for _, p := range ports {
		portStrs = append(portStrs, strconv.Itoa(int(p)))
	}
	rootSess := strings.TrimSpace(auth.RootSessionID)
	if rootSess == "" {
		rootSess = strings.TrimSpace(owner.SessionID)
	}
	return fmt.Sprintf("proj=%s|root=%s|ws=%s|lb=%t|ports=%s",
		strings.TrimSpace(owner.ProjectID),
		rootSess,
		strings.TrimSpace(auth.WorkspaceID),
		auth.LoopbackGranted || len(ports) > 0,
		strings.Join(portStrs, ","),
	)
}

func buildProbeKey(owner Owner, auth Authority, cond awaitstore.Condition) (ProbeKey, bool) {
	domain := buildAuthorityDomain(owner, auth)
	switch cond.Kind {
	case "http_ready":
		method := strings.ToUpper(strings.TrimSpace(cond.Method))
		if method == "" {
			method = "GET"
		}
		sMin, sMax := cond.StatusMin, cond.StatusMax
		if sMin == 0 {
			sMin = 200
		}
		if sMax == 0 {
			sMax = 399
		}
		target := fmt.Sprintf("%s|%s|%d..%d", method, strings.TrimSpace(cond.URL), sMin, sMax)
		return ProbeKey{AuthorityDomain: domain, Kind: "http_ready", Target: target}, true
	case "port_ready":
		host := strings.ToLower(strings.TrimSpace(cond.Host))
		if host == "" {
			host = "localhost"
		}
		target := fmt.Sprintf("%s:%d", host, cond.Port)
		return ProbeKey{AuthorityDomain: domain, Kind: "port_ready", Target: target}, true
	default:
		return ProbeKey{}, false
	}
}

func probeGroupID(key ProbeKey) string {
	sum := sha256.Sum256([]byte(key.AuthorityDomain + "\x00" + key.Kind + "\x00" + key.Target))
	return "probe:" + hex.EncodeToString(sum[:12])
}

func subscribeWatchSampler(s *Store, reg Registration, now time.Time) {
	if s == nil || s.sampler == nil || reg.Lifecycle.State != StateLive {
		return
	}
	s.sampler.unsubscribeWatch(reg.WatchID, s.heap)

	var scheduledIDs []string
	var dueTimes []time.Time

	s.sampler.mu.Lock()
	for i, cond := range reg.Conditions {
		key, ok := buildProbeKey(reg.Owner, reg.Authority, cond)
		if !ok {
			continue
		}
		if s.sampler.subscriberCountLocked() >= MaxSamplerSubscribers {
			break
		}
		gid := probeGroupID(key)
		grp, exists := s.sampler.groupsByID[gid]
		if !exists {
			grp = &probeGroup{
				id:          gid,
				key:         key,
				condition:   cond,
				authority:   reg.Authority,
				owner:       reg.Owner,
				toolCallID:  reg.InvocationID,
				interval:    SamplerMinInterval,
				subscribers: make(map[string]probeSubscriber),
			}
			s.sampler.groupsByID[gid] = grp
		} else {
			grp.interval = SamplerMinInterval
		}
		subID := fmt.Sprintf("%s:%d", reg.WatchID, i)
		grp.subscribers[subID] = probeSubscriber{watchID: reg.WatchID, altIdx: i}
		s.sampler.watchToIDs[reg.WatchID] = append(s.sampler.watchToIDs[reg.WatchID], gid)
		scheduledIDs = append(scheduledIDs, gid)
		dueTimes = append(dueTimes, now.Add(SamplerMinInterval))
	}
	s.sampler.mu.Unlock()

	for idx, gid := range scheduledIDs {
		s.heap.Upsert(KindSamplerProbe, gid, dueTimes[idx])
	}
}

func (sm *Sampler) subscriberCountLocked() int {
	total := 0
	for _, g := range sm.groupsByID {
		total += len(g.subscribers)
	}
	return total
}

func (sm *Sampler) unsubscribeWatch(watchID string, heap *DeadlineHeap) {
	if sm == nil || watchID == "" {
		return
	}
	var emptyIDs []string
	sm.mu.Lock()
	gids := sm.watchToIDs[watchID]
	delete(sm.watchToIDs, watchID)
	for _, gid := range gids {
		grp, ok := sm.groupsByID[gid]
		if !ok {
			continue
		}
		for subID, sub := range grp.subscribers {
			if sub.watchID == watchID {
				delete(grp.subscribers, subID)
			}
		}
		if len(grp.subscribers) == 0 {
			delete(sm.groupsByID, gid)
			delete(sm.queued, gid)
			emptyIDs = append(emptyIDs, gid)
		}
	}
	sm.mu.Unlock()

	for _, gid := range emptyIDs {
		heap.Remove(KindSamplerProbe, gid)
	}
}

func enqueueDueProbeGroups(s *Store, groupIDs []string) {
	if s == nil || s.sampler == nil || len(groupIDs) == 0 {
		return
	}
	s.sampler.mu.Lock()
	for _, gid := range groupIDs {
		if _, exists := s.sampler.groupsByID[gid]; exists && !s.sampler.queued[gid] {
			s.sampler.queued[gid] = true
			s.sampler.rrQueue = append(s.sampler.rrQueue, gid)
		}
	}
	s.sampler.mu.Unlock()
}

func stepSamplerRoundRobin(s *Store, ctx context.Context, now time.Time) error {
	if s == nil || s.sampler == nil {
		return nil
	}
	var selected []*probeGroup
	var deferred []string

	s.sampler.mu.Lock()
	for len(s.sampler.rrQueue) > 0 && len(selected) < MaxConcurrentProbes {
		gid := s.sampler.rrQueue[0]
		s.sampler.rrQueue = s.sampler.rrQueue[1:]
		delete(s.sampler.queued, gid)
		if grp, ok := s.sampler.groupsByID[gid]; ok && len(grp.subscribers) > 0 {
			cp := *grp
			cp.subscribers = make(map[string]probeSubscriber, len(grp.subscribers))
			for k, v := range grp.subscribers {
				cp.subscribers[k] = v
			}
			selected = append(selected, &cp)
		}
	}
	deferred = append(deferred, s.sampler.rrQueue...)
	s.sampler.mu.Unlock()

	for _, gid := range deferred {
		s.heap.Upsert(KindSamplerProbe, gid, now.Add(SamplerMinInterval))
	}
	if len(selected) == 0 {
		return nil
	}

	type resultItem struct {
		grp *probeGroup
		out probeOutcome
	}
	results := make([]resultItem, len(selected))
	var wg sync.WaitGroup
	for idx, grp := range selected {
		wg.Add(1)
		go func(i int, g *probeGroup) {
			defer wg.Done()
			results[i] = resultItem{grp: g, out: executeReadinessProbe(ctx, g)}
		}(idx, grp)
	}
	wg.Wait()

	for _, item := range results {
		if err := applyProbeGroupResult(s, ctx, item.grp, item.out, now); err != nil {
			return err
		}
	}
	return nil
}

func applyProbeGroupResult(s *Store, ctx context.Context, snapshot *probeGroup, out probeOutcome, now time.Time) error {
	s.sampler.mu.Lock()
	liveGrp, stillExists := s.sampler.groupsByID[snapshot.id]
	if stillExists {
		liveGrp.probeCount++
		s.sampler.totalRuns++
	}
	s.sampler.mu.Unlock()

	subs := make([]probeSubscriber, 0, len(snapshot.subscribers))
	for _, sub := range snapshot.subscribers {
		subs = append(subs, sub)
	}
	sort.Slice(subs, func(i, j int) bool {
		if subs[i].watchID != subs[j].watchID {
			return subs[i].watchID < subs[j].watchID
		}
		return subs[i].altIdx < subs[j].altIdx
	})

	stateChanged := false
	for _, sub := range subs {
		changed, err := applySubscriberProbeOutcome(s, ctx, sub, snapshot.condition, out)
		if err != nil {
			return err
		}
		if changed {
			stateChanged = true
		}
	}

	var nextDue time.Time
	s.sampler.mu.Lock()
	if grp, ok := s.sampler.groupsByID[snapshot.id]; ok && len(grp.subscribers) > 0 {
		if stateChanged {
			grp.interval = SamplerMinInterval
		} else if grp.interval < SamplerMaxInterval {
			grp.interval *= 2
			if grp.interval > SamplerMaxInterval {
				grp.interval = SamplerMaxInterval
			}
		}
		nextDue = now.Add(grp.interval)
	}
	s.sampler.mu.Unlock()

	if !nextDue.IsZero() {
		s.heap.Upsert(KindSamplerProbe, snapshot.id, nextDue)
	}
	return nil
}

func readinessCursorKeys(altIdx int) (armedKey, epKey string) {
	return fmt.Sprintf("ready:armed:%d", altIdx), fmt.Sprintf("ready:ep:%d", altIdx)
}

func applySubscriberProbeOutcome(s *Store, ctx context.Context, sub probeSubscriber, cond awaitstore.Condition, out probeOutcome) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, sub.watchID)
	_ = tx.Rollback()
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reg.Lifecycle.State != StateLive {
		s.sampler.unsubscribeWatch(reg.WatchID, s.heap)
		return false, nil
	}

	if allowed, reason := checkWatchAuthority(s, ctx, reg); !allowed {
		return true, revokeWatchAuthority(s, ctx, reg, cond.Kind, reason)
	}
	if out.Denied {
		return true, revokeWatchAuthority(s, ctx, reg, cond.Kind, out.Code)
	}
	// Unknown or inaccessible probe state neither proves readiness nor re-arms a level predicate.
	if !out.Known {
		return false, nil
	}

	armedKey, epKey := readinessCursorKeys(sub.altIdx)
	armed := reg.Cursors[armedKey] != "false"

	if !out.Satisfied {
		if !armed {
			return true, updateWatchCursor(s, ctx, reg.WatchID, armedKey, "true")
		}
		return false, nil
	}
	if !armed {
		return false, nil
	}

	ep, _ := strconv.Atoi(reg.Cursors[epKey])
	ep++
	occID := fmt.Sprintf("%s:%d:ep%d", cond.Kind, sub.altIdx, ep)
	ev := map[string]any{
		"kind": cond.Kind,
	}
	if cond.Kind == "http_ready" {
		ev["url"] = cond.URL
		ev["method"] = cond.Method
		ev["status"] = out.Status
	} else {
		ev["host"] = cond.Host
		ev["port"] = cond.Port
	}
	_, _, err = s.RecordMatch(ctx, reg.WatchID, MatchInput{
		SourceKind:   cond.Kind,
		OccurrenceID: occID,
		Outcome:      "satisfied",
		Evidence:     ev,
	}, map[string]string{
		armedKey: "false",
		epKey:    strconv.Itoa(ep),
	})
	return true, err
}

func updateWatchCursor(s *Store, ctx context.Context, watchID, key, val string) error {
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE watch_registrations SET cursors_json = json_set(cursors_json, ?, ?), updated_at = ? WHERE watch_id = ? AND state = 'live'`,
		"$."+key, val, nowStr, watchID)
	return err
}

func revokeWatchAuthority(s *Store, ctx context.Context, reg Registration, sourceKind, code string) error {
	ref := "restricted:" + reg.WatchID
	if code == "" {
		code = "AUTHORITY_REVOKED"
	}
	_, _, _ = s.RecordMatch(ctx, reg.WatchID, MatchInput{
		SourceKind:   sourceKind,
		OccurrenceID: fmt.Sprintf("revoked:%s:%s", sourceKind, reg.WatchID),
		Outcome:      "revoked",
		RetainedRef:  ref,
		Restricted:   true,
		Evidence: map[string]any{
			"kind":   sourceKind,
			"code":   code,
			"status": "revoked",
		},
	}, nil)
	return s.MarkSourceLost(ctx, reg.WatchID)
}

func executeReadinessProbe(ctx context.Context, grp *probeGroup) probeOutcome {
	switch grp.condition.Kind {
	case "http_ready":
		return probeHTTPReady(ctx, grp)
	case "port_ready":
		return probePortReady(ctx, grp)
	default:
		return probeOutcome{}
	}
}

func probeHTTPReady(ctx context.Context, grp *probeGroup) probeOutcome {
	cond := grp.condition
	parsedURL, err := url.Parse(cond.URL)
	if err != nil {
		return probeOutcome{}
	}
	isLoopback := egress.SyntacticLoopback(parsedURL.Hostname())
	if isLoopback && !loopbackURLCovered(grp.authority, parsedURL) {
		return probeOutcome{Denied: true, Code: "LOOPBACK_PORT_REVOKED"}
	}
	rootSess := strings.TrimSpace(grp.authority.RootSessionID)
	if rootSess == "" {
		rootSess = grp.owner.SessionID
	}
	toolCallID := strings.TrimSpace(grp.toolCallID)
	if toolCallID == "" {
		toolCallID = grp.id
	}
	cmd := confine.EgressCommand{
		SessionID:     grp.owner.SessionID,
		RootSessionID: rootSess,
		ProjectID:     grp.owner.ProjectID,
		ProjectDir:    grp.authority.ProjectDir,
		ToolCallID:    toolCallID,
		Image:         "watch",
	}
	defer confine.ForgetEgressAction(cmd.SessionID, cmd.ToolCallID)

	reqCtx := egressgate.WithAttribution(ctx, cmd)
	method := strings.ToUpper(strings.TrimSpace(cond.Method))
	if method == "" {
		method = "GET"
	}
	sMin, sMax := cond.StatusMin, cond.StatusMax
	if sMin == 0 {
		sMin = 200
	}
	if sMax == 0 {
		sMax = 399
	}
	resp, err := outboundhttp.Do(reqCtx, outboundhttp.Request{
		Class:        egressclass.AgentHTTPRequest,
		Method:       method,
		URL:          cond.URL,
		Timeout:      2 * time.Second,
		Redirects:    outboundhttp.RedirectNone,
		DiscardBody:  true,
		AllowAddress: samplerLoopbackPolicy(grp.authority),
		BeforeHop: func(hopCtx context.Context, hopURL *url.URL) error {
			return egressgate.AwaitHTTPRequest(hopCtx, hopURL, method)
		},
	})
	if _, denied := egressgate.Denied(reqCtx); denied {
		return probeOutcome{Denied: true, Code: "HTTP_REQUEST_HOST_DENIED"}
	}
	if err != nil {
		// On loopback, connection refusal is an observed not-ready state; DNS/context errors stay unknown.
		if isLoopback && ctx.Err() == nil {
			return probeOutcome{Known: true, Satisfied: false}
		}
		return probeOutcome{Known: false}
	}
	ok := resp.Status >= sMin && resp.Status <= sMax
	return probeOutcome{Known: true, Satisfied: ok, Status: resp.Status}
}

func probePortReady(ctx context.Context, grp *probeGroup) probeOutcome {
	cond := grp.condition
	if !samplerPortCovered(grp.authority, cond.Port) {
		return probeOutcome{Denied: true, Code: "LOOPBACK_PORT_REVOKED"}
	}
	host := strings.TrimSpace(cond.Host)
	if host == "" {
		host = "localhost"
	}
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return probeOutcome{Known: false}
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	sawLoopback := false
	for _, addr := range addrs {
		if !addr.IsLoopback() {
			return probeOutcome{Denied: true, Code: "NON_LOOPBACK_TARGET"}
		}
		sawLoopback = true
		conn, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(cond.Port))))
		if dialErr == nil {
			_ = conn.Close()
			return probeOutcome{Known: true, Satisfied: true}
		}
	}
	if sawLoopback && ctx.Err() == nil {
		return probeOutcome{Known: true, Satisfied: false}
	}
	return probeOutcome{Known: false}
}

func samplerLoopbackPolicy(auth Authority) func(netip.Addr, uint16) bool {
	return func(addr netip.Addr, port uint16) bool {
		return addr.IsLoopback() && samplerPortCovered(auth, port)
	}
}

func loopbackURLCovered(auth Authority, u *url.URL) bool {
	port := 80
	if strings.EqualFold(u.Scheme, "https") {
		port = 443
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil {
			return false
		}
		port = p
	}
	return samplerPortCovered(auth, uint16(port))
}

func samplerPortCovered(auth Authority, port uint16) bool {
	if len(auth.LoopbackPorts) > 0 {
		for _, allowed := range auth.LoopbackPorts {
			if allowed == port {
				return true
			}
		}
		return false
	}
	return auth.LoopbackGranted
}
