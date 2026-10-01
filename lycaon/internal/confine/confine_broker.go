package confine

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// brokerPortsFile records the listener ports, so an address handed out by an
// earlier run still answers.
const brokerPortsFile = "egress-broker.json"

type brokerPorts struct {
	HTTP  uint16 `json:"http_port"`
	SOCKS uint16 `json:"socks_port"`
}

var broker = struct {
	mu sync.RWMutex
	// bound reports that a front door is serving, whether real or installed by a test.
	bound  bool
	live   *egressproxy.Broker
	addrs  egressproxy.Addrs
	failed error
}{}

// StartEgressBroker binds the host's single mediation front door. Its address is
// the same for every project, action, and invocation, so a confined process's
// environment follows its boundary rather than the call that produced it.
func StartEgressBroker(stateRoot string) (egressproxy.Addrs, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.bound {
		return broker.addrs, nil
	}
	b := egressproxy.New(egressBroker.decideEndpoint)
	b.SetPeerResolver(egressBroker.resolvePeer)
	b.SetDialReporter(egressBroker.reportDialOutcome)
	b.SetHTTPReporter(egressBroker.reportHTTPOutcome)
	b.SetLoopbackConnectAuthority(egressBroker.loopbackFor)
	b.SetLeaseCheck(egressBroker.leased)
	b.SetRefusalReporter(egressBroker.reportRefusal)

	recorded := readBrokerPorts(stateRoot)
	addrs, err := b.Start(egressproxy.Options{HTTPPort: recorded.HTTP, SOCKSPort: recorded.SOCKS})
	if err != nil {
		broker.failed = err
		recordEgressDegraded("broker_start_failed", err)
		return egressproxy.Addrs{}, fmt.Errorf("start egress broker: %w", err)
	}
	if addrs.HTTP == "" || addrs.SOCKS == "" {
		_ = b.Close()
		err := fmt.Errorf("egress broker started without both listener addresses")
		broker.failed = err
		recordEgressDegraded("broker_empty_addr", err)
		return egressproxy.Addrs{}, err
	}
	broker.live, broker.addrs, broker.bound = b, addrs, true
	writeBrokerPorts(stateRoot, addrs)
	return addrs, nil
}

// StopEgressBroker closes the front door. Running processes lose mediated
// egress; their next dial reports an ended lease.
func StopEgressBroker() error {
	broker.mu.Lock()
	live := broker.live
	broker.live, broker.addrs, broker.bound = nil, egressproxy.Addrs{}, false
	broker.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Close()
}

func brokerAddrs() (egressproxy.Addrs, error) {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	if !broker.bound {
		if broker.failed != nil {
			return egressproxy.Addrs{}, fmt.Errorf("egress broker is unavailable: %w", broker.failed)
		}
		return egressproxy.Addrs{}, fmt.Errorf("egress broker is not running")
	}
	return broker.addrs, nil
}

// SetBrokerForTest installs a front door without binding real listeners.
func SetBrokerForTest(b *egressproxy.Broker, addrs egressproxy.Addrs) func() {
	return setBrokerState(b, addrs, true, nil)
}

// SetBrokerUnavailableForTest makes mediation fail to start, so a caller can
// check that an action refuses rather than launching unmediated.
func SetBrokerUnavailableForTest(cause error) func() {
	restore := setBrokerState(nil, egressproxy.Addrs{}, false, cause)
	recordEgressDegraded("broker_start_failed", cause)
	return restore
}

func setBrokerState(b *egressproxy.Broker, addrs egressproxy.Addrs, bound bool, failed error) func() {
	broker.mu.Lock()
	prevLive, prevAddrs, prevFailed, prevBound := broker.live, broker.addrs, broker.failed, broker.bound
	broker.live, broker.addrs, broker.failed, broker.bound = b, addrs, failed, bound
	broker.mu.Unlock()
	egressDegradedMu.Lock()
	egressDegraded = nil
	egressDegradedMu.Unlock()
	return func() {
		broker.mu.Lock()
		broker.live, broker.addrs, broker.failed, broker.bound = prevLive, prevAddrs, prevFailed, prevBound
		broker.mu.Unlock()
		egressDegradedMu.Lock()
		egressDegraded = nil
		egressDegradedMu.Unlock()
	}
}

func brokerPortsPath(stateRoot string) string {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return ""
	}
	return filepath.Join(stateRoot, brokerPortsFile)
}

func readBrokerPorts(stateRoot string) brokerPorts {
	path := brokerPortsPath(stateRoot)
	if path == "" {
		return brokerPorts{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return brokerPorts{}
	}
	var ports brokerPorts
	if json.Unmarshal(raw, &ports) != nil {
		return brokerPorts{}
	}
	return ports
}

func writeBrokerPorts(stateRoot string, addrs egressproxy.Addrs) {
	path := brokerPortsPath(stateRoot)
	if path == "" {
		return
	}
	ports := brokerPorts{HTTP: portOf(addrs.HTTP), SOCKS: portOf(addrs.SOCKS)}
	if ports.HTTP == 0 || ports.SOCKS == 0 {
		return
	}
	raw, err := json.Marshal(ports)
	if err != nil {
		return
	}
	_, _ = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   strings.NewReader(string(raw)),
		Mode:     0o600,
		DirMode:  0o700,
	})
}

func portOf(addr string) uint16 {
	parsed, err := netip.ParseAddrPort(strings.TrimSpace(addr))
	if err != nil {
		return 0
	}
	return parsed.Port()
}
