package confine

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/lineage"
)

// ActionLease owns one action's lineage, egress lease, and refusal feed.
type ActionLease struct {
	line *lineage.Lineage
	cmd  EgressCommand
	// mediated records whether this action registered a broker lease.
	mediated bool
	once     sync.Once
	hosts    []EgressHost
	// refusalTag names the action in its profile's deny rules.
	refusalTag string
	refusals   *refusalFeed
	settleOnce sync.Once
}

// BindAction binds a confinement to one action before it runs. Every confined
// launch gets a lineage; a proxy-only one also registers the broker lease that
// makes its dials attributable.
func BindAction(c *Confinement, cmd EgressCommand) (*ActionLease, error) {
	lease := &ActionLease{cmd: cmd}
	if c == nil {
		return lease, nil
	}
	if c.LineageID != "" || c.refusalTag != "" {
		// Rebinding would detach the existing action's descendants.
		return nil, fmt.Errorf("confinement is already bound to an action")
	}
	lease.watchRefusals(c)
	if !lineage.Supported() {
		// Mediated egress requires descendant attribution.
		if c.Network == NetworkProxyOnly {
			lease.unwatchRefusals(c)
			return nil, fmt.Errorf("mediated egress requires descendant observation on this platform")
		}
		return lease, nil
	}
	line, err := lineage.Open(actionSubject(cmd), cmd.SessionID)
	if err != nil {
		lease.unwatchRefusals(c)
		return nil, fmt.Errorf("open action lineage: %w", err)
	}
	lease.line = line
	c.lineage = line
	c.LineageID = string(line.ID())
	refusalWatch.bindLineage(lease.refusalTag, c.LineageID)

	if c.Network != NetworkProxyOnly {
		return lease, nil
	}
	addrs, err := brokerAddrs()
	if err != nil {
		_ = line.Close()
		c.lineage, c.LineageID = nil, ""
		lease.unwatchRefusals(c)
		return nil, err
	}
	egressBroker.mu.Lock()
	egressBroker.tokens[c.LineageID] = cmd
	egressBroker.loopback[c.LineageID] = loopbackConnectAuthority(c)
	egressBroker.rememberLineageLocked(c.LineageID, cmd)
	egressBroker.mu.Unlock()
	lease.mediated = true
	c.ProxyAddr = addrs.HTTP
	c.SocksProxyAddr = addrs.SOCKS
	return lease, nil
}

// watchRefusals tags deny rules to route kernel reports to this lease.
func (l *ActionLease) watchRefusals(c *Confinement) {
	if c.HostExecution {
		return
	}
	rules, err := filesystemRules(*c)
	if err != nil {
		return
	}
	l.refusalTag = newRefusalNonce()
	l.refusals = refusalWatch.register(l.refusalTag, rules)
	c.refusalTag = l.refusalTag
}

func (l *ActionLease) unwatchRefusals(c *Confinement) {
	if l.refusalTag == "" {
		return
	}
	refusalWatch.unregister(l.refusalTag)
	c.refusalTag, l.refusalTag, l.refusals = "", "", nil
}

// Refusals is what the kernel has reported refusing this action so far.
func (l *ActionLease) Refusals() SandboxRefusals {
	if l == nil || l.refusals == nil {
		return SandboxRefusals{}
	}
	return l.refusals.snapshot()
}

// SettledRefusals waits for the kernel's last reports of an ended action, then
// returns them. Call it once the action's processes have exited.
func (l *ActionLease) SettledRefusals(ctx context.Context) SandboxRefusals {
	if l == nil || l.refusals == nil {
		return SandboxRefusals{}
	}
	l.settleRefusals(ctx)
	return l.refusals.snapshot()
}

// OnRefusal calls fn after each distinct refusal the kernel reports.
func (l *ActionLease) OnRefusal(fn func()) {
	if l == nil || l.refusals == nil || fn == nil {
		return
	}
	l.refusals.listen(fn)
}

func (l *ActionLease) settleRefusals(ctx context.Context) {
	l.settleOnce.Do(func() {
		refusalWatch.settle(ctx, l.refusals)
		refusalWatch.unregister(l.refusalTag)
	})
}

// actionSubject is what a refusal quotes back to the person.
func actionSubject(cmd EgressCommand) string {
	name := strings.TrimSpace(cmd.ToolName)
	if name == "" {
		name = "command"
	}
	line := strings.TrimSpace(cmd.CommandLine)
	if line == "" {
		line = strings.TrimSpace(cmd.Image)
	}
	if line == "" {
		return name
	}
	// Reads as a sentence fragment inside a refusal message.
	return name + " running " + firstWords(line)
}

// firstWords keeps a refusal short without quoting a whole command line.
func firstWords(line string) string {
	fields := strings.Fields(line)
	if len(fields) > 4 {
		fields = append(fields[:4], "…")
	}
	return strings.Join(fields, " ")
}

// LineageID identifies this action's descendants.
func (l *ActionLease) LineageID() string {
	if l == nil {
		return ""
	}
	return string(l.line.ID())
}

// ObservedHosts returns broker-attributed destinations.
func (l *ActionLease) ObservedHosts() []EgressHost {
	if l == nil || !l.mediated {
		return nil
	}
	key := l.LineageID()
	// Close preserves the final observations under this lock.
	egressBroker.mu.Lock()
	defer egressBroker.mu.Unlock()
	if live := egressBroker.egress[key]; len(live) > 0 {
		return append([]EgressHost(nil), live...)
	}
	return append([]EgressHost(nil), l.hosts...)
}

// Survivors reports descendants still running as the action ends.
func (l *ActionLease) Survivors() ([]int, error) {
	if l == nil {
		return nil, nil
	}
	return l.line.Survivors()
}

// Close releases the action's leases and retains its final observations.
func (l *ActionLease) Close(ctx context.Context) []EgressHost {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.refusals != nil {
			l.settleRefusals(ctx)
		}
		if l.mediated {
			key := l.LineageID()
			egressBroker.mu.Lock()
			l.hosts = append([]EgressHost(nil), egressBroker.egress[key]...)
			delete(egressBroker.egress, key)
			delete(egressBroker.tokens, key)
			delete(egressBroker.loopback, key)
			egressBroker.forgetActionLocked(l.cmd.SessionID, key)
			egressBroker.mu.Unlock()
		}
		if l.line != nil {
			_ = l.line.Close()
		}
	})
	return append([]EgressHost(nil), l.hosts...)
}

// EgressBound reports whether a proxy confinement has a live action lease.
func EgressBound(c *Confinement) bool {
	if c == nil || c.Network != NetworkProxyOnly || c.LineageID == "" {
		return false
	}
	return egressBroker.leased(c.LineageID)
}

func (b *egressBrokerT) leased(lineageID string) bool {
	if lineageID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.tokens[lineageID]
	return ok
}

// resolvePeer identifies the process on a broker connection. Finished actions
// fall back to a live action for the same project.
func (b *egressBrokerT) resolvePeer(local, remote netip.AddrPort) (egressproxy.Peer, bool) {
	line, ok := lineage.OfPeer(local, remote)
	if !ok {
		return egressproxy.Peer{}, false
	}
	id := string(line.ID())
	peer := egressproxy.Peer{Lineage: id, Owner: line.Owner()}
	if line.State() == lineage.StateLive && b.leased(id) {
		peer.Leased = true
		return peer, true
	}
	if successor, found := b.successorFor(id); found {
		peer.Lineage = successor
		peer.Leased = true
		peer.Inherited = true
		return peer, true
	}
	return peer, true
}

// successorFor finds a live action of the same project to answer for a process
// an earlier action left running.
func (b *egressBrokerT) successorFor(retired string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	origin, known := b.lineages[retired]
	project := strings.TrimSpace(origin.ProjectID)
	if !known || project == "" {
		return "", false
	}
	// The oldest live action answers, so concurrent commands resolve to one
	// stable owner rather than whichever dialed first.
	best, bestSeq := "", uint64(0)
	for id, cmd := range b.tokens {
		if strings.TrimSpace(cmd.ProjectID) != project {
			continue
		}
		seq := b.lineageSeq[id]
		if best == "" || seq < bestSeq {
			best, bestSeq = id, seq
		}
	}
	return best, best != ""
}

// rememberLineageLocked retains an action's identity past its lease so a process
// it left running can still be placed.
func (b *egressBrokerT) rememberLineageLocked(id string, cmd EgressCommand) {
	b.lineageOrder++
	b.lineageSeq[id] = b.lineageOrder
	b.lineages[id] = cmd
	for len(b.lineageAge) >= retainedLineages {
		oldest := b.lineageAge[0]
		b.lineageAge = b.lineageAge[1:]
		if _, live := b.tokens[oldest]; !live {
			delete(b.lineages, oldest)
			delete(b.lineageSeq, oldest)
		}
	}
	b.lineageAge = append(b.lineageAge, id)
}

// loopbackFor reads the local-port grant of one action.
func (b *egressBrokerT) loopbackFor(lineageID string, port uint16) bool {
	b.mu.Lock()
	grant := b.loopback[lineageID]
	b.mu.Unlock()
	return grant != nil && grant(port)
}

// LineageMarkerForTest returns the descendant marker a confined launch hands its
// child, so a test can start a descendant without a full spawn.
func LineageMarkerForTest(c *Confinement) *os.File {
	if c == nil {
		return nil
	}
	return c.lineage.ChildFile()
}
