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
	mu      sync.RWMutex
	live    *egressproxy.Broker
	addrs   egressproxy.Addrs
	failure brokerFailure
}{}

type brokerFailure struct {
	reason string
	err    error
}

// StartEgressBroker binds the shared HTTP and SOCKS listeners.
func StartEgressBroker(stateRoot string, listen egressproxy.ListenFunc) (egressproxy.Addrs, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.live != nil {
		return broker.addrs, nil
	}
	b := egressproxy.New(egressBroker.decideEndpoint)
	b.SetPeerResolver(egressBroker.resolvePeer)
	b.SetDialReporter(egressBroker.reportDialOutcome)
	b.SetHTTPReporter(egressBroker.reportHTTPOutcome)
	b.SetLoopbackConnectAuthority(egressBroker.loopbackFor)
	b.SetOwnedLoopbackAuthority(egressBroker.ownedLoopbackFor)
	b.SetLoopbackRefusalReporter(egressBroker.reportLoopbackOwnershipRefusal)
	b.SetLeaseCheck(egressBroker.leased)
	b.SetRefusalReporter(egressBroker.reportRefusal)

	recorded := readBrokerPorts(stateRoot)
	addrs, err := b.Start(egressproxy.Options{HTTPPort: recorded.HTTP, SOCKSPort: recorded.SOCKS, ListenHTTP: listen})
	if err != nil {
		setBrokerFailure("broker_start_failed", err)
		return egressproxy.Addrs{}, fmt.Errorf("start egress broker: %w", err)
	}
	if addrs.HTTP == "" || addrs.SOCKS == "" {
		_ = b.Close()
		err := fmt.Errorf("egress broker started without both listener addresses")
		setBrokerFailure("broker_empty_addr", err)
		return egressproxy.Addrs{}, err
	}
	broker.live, broker.addrs, broker.failure = b, addrs, brokerFailure{}
	writeBrokerPorts(stateRoot, addrs)
	return addrs, nil
}

// StopEgressBroker closes the shared listeners and active connections.
func StopEgressBroker() error {
	broker.mu.Lock()
	live := broker.live
	broker.live, broker.addrs, broker.failure = nil, egressproxy.Addrs{}, brokerFailure{}
	broker.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Close()
}

func brokerAddrs() (egressproxy.Addrs, error) {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	if broker.live == nil {
		if broker.failure.err != nil {
			return egressproxy.Addrs{}, fmt.Errorf("egress broker is unavailable: %w", broker.failure.err)
		}
		return egressproxy.Addrs{}, fmt.Errorf("egress broker is not running")
	}
	return broker.addrs, nil
}

func setBrokerFailure(reason string, err error) {
	if broker.failure.err == nil {
		logBrokerStartFailure(reason, err.Error())
	}
	broker.failure = brokerFailure{reason: reason, err: err}
}

// EgressDegradedState reports the current broker startup failure.
func EgressDegradedState() (EgressDegraded, bool) {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	return EgressDegraded{Reason: broker.failure.reason}, broker.failure.err != nil
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
