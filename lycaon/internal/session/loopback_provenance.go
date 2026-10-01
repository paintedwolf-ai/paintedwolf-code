package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/lineage"
)

// ProvenanceMethod names the structured fact that decided a port's ownership.
type ProvenanceMethod string

const (
	// ProvenanceLineage: every listener descends from an action of this session.
	ProvenanceLineage ProvenanceMethod = "process_lineage"
	// ProvenanceContainer: the container engine reports the port published by a
	// container whose compose working directory is this workspace.
	ProvenanceContainer ProvenanceMethod = "container_workspace"
	// ProvenancePreExisting: the port was listening when the engine started.
	ProvenancePreExisting ProvenanceMethod = "pre_existing_host"
	// ProvenanceExposed: a session-owned listener is reachable beyond loopback.
	ProvenanceExposed ProvenanceMethod = "external_exposure"
	// ProvenanceUnverified: no structured fact establishes ownership.
	ProvenanceUnverified ProvenanceMethod = "unverified"
)

// ProvenanceEvidence describes how session ownership was decided.
type ProvenanceEvidence struct {
	Method      ProvenanceMethod
	OwnerID     string // PID or container name
	Description string
}

// LoopbackProvenanceResolver decides whether a loopback port belongs to a session.
type LoopbackProvenanceResolver interface {
	IsSessionOwned(ctx context.Context, sessionID, workspaceDir string, port uint16) (bool, ProvenanceEvidence)
	// HeldByOther reports whether a listener the session does not own holds port,
	// or whether the host cannot tell.
	HeldByOther(ctx context.Context, sessionID, workspaceDir string, port uint16) bool
	// RecordSessionContainer records an invocation-launched container ID for a session.
	RecordSessionContainer(sessionID, containerID, socketPath string)
	// ForgetSession clears recorded container provenance for a deleted session.
	ForgetSession(sessionID string)
}

// socketListener is one process holding one listening TCP socket. PID is zero
// when the holder cannot be inspected; Addr is invalid when the bind address
// could not be read.
type socketListener struct {
	PID  int
	Port uint16
	Addr netip.Addr
}

// loopbackBound reports whether the listener accepts only local connections.
func (l socketListener) loopbackBound() bool {
	return l.Addr.IsValid() && l.Addr.Unmap().IsLoopback()
}

// DefaultLoopbackProvenance attributes listeners through launch lineage and the
// container engine. It never infers ownership from timing or workspace files.
type DefaultLoopbackProvenance struct {
	// baseline holds the ports listening at engine start; nil when that
	// snapshot failed, so absence from it proves nothing.
	baseline map[uint16]bool
	// listeners reads the host's listening TCP sockets.
	listeners func(ctx context.Context) ([]socketListener, error)
	// lineageSession names the session whose action launched pid.
	lineageSession func(pid int) (string, bool)
	// containers lists running containers from every reachable engine.
	containers        func(ctx context.Context) []dockerContainerInfo
	mu                sync.RWMutex
	sessionContainers map[string][]string
}

// NewLoopbackProvenance snapshots the listening ports present at engine start.
func NewLoopbackProvenance() *DefaultLoopbackProvenance {
	sockets := discoverDockerSockets()
	return newLoopbackProvenance(hostListeners, lineageSessionOf, func(ctx context.Context) []dockerContainerInfo {
		return queryDockerContainers(ctx, sockets)
	})
}

func newLoopbackProvenance(
	listeners func(context.Context) ([]socketListener, error),
	lineageSession func(int) (string, bool),
	containers func(context.Context) []dockerContainerInfo,
) *DefaultLoopbackProvenance {
	p := &DefaultLoopbackProvenance{listeners: listeners, lineageSession: lineageSession, containers: containers}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if current, err := listeners(ctx); err == nil {
		p.baseline = make(map[uint16]bool, len(current))
		for _, l := range current {
			p.baseline[l.Port] = true
		}
	}
	return p
}

func lineageSessionOf(pid int) (string, bool) {
	line, ok := lineage.Of(pid)
	if !ok {
		return "", false
	}
	return line.Session(), true
}

// IsSessionOwned decides whether port belongs to the session and binds only to loopback.
func (p *DefaultLoopbackProvenance) IsSessionOwned(ctx context.Context, sessionID, workspaceDir string, port uint16) (bool, ProvenanceEvidence) {
	if p == nil || port == 0 {
		return false, ProvenanceEvidence{Method: ProvenanceUnverified, Description: "unspecified port or resolver"}
	}
	current, err := p.listeners(ctx)
	if err != nil {
		current = nil
	}
	return p.ownership(ctx, sessionID, workspaceDir, port, onPort(current, port))
}

// HeldByOther implements LoopbackProvenanceResolver.
func (p *DefaultLoopbackProvenance) HeldByOther(ctx context.Context, sessionID, workspaceDir string, port uint16) bool {
	if p == nil || port == 0 {
		return true
	}
	current, err := p.listeners(ctx)
	if err != nil {
		return true
	}
	held := onPort(current, port)
	if len(held) == 0 {
		return false
	}
	owned, _ := p.ownership(ctx, sessionID, workspaceDir, port, held)
	return !owned
}

func (p *DefaultLoopbackProvenance) ownership(ctx context.Context, sessionID, workspaceDir string, port uint16, held []socketListener) (bool, ProvenanceEvidence) {
	if owner, ok := p.lineageOwner(sessionID, held); ok {
		for _, l := range held {
			if !l.loopbackBound() {
				return false, ProvenanceEvidence{
					Method: ProvenanceExposed, OwnerID: strconv.Itoa(l.PID),
					Description: fmt.Sprintf("listener process %d on port %d is reachable beyond loopback", l.PID, port),
				}
			}
		}
		return true, ProvenanceEvidence{
			Method: ProvenanceLineage, OwnerID: strconv.Itoa(owner),
			Description: fmt.Sprintf("listener process %d on port %d was launched by session %s", owner, port, sessionID),
		}
	}

	// Container attribution rests on the port having appeared after start.
	if p.baseline == nil {
		return false, ProvenanceEvidence{
			Method:      ProvenanceUnverified,
			Description: fmt.Sprintf("port %d cannot be attributed: the listeners present at start are unknown", port),
		}
	}
	if p.baseline[port] {
		return false, ProvenanceEvidence{
			Method:      ProvenancePreExisting,
			Description: fmt.Sprintf("port %d was listening before the engine started", port),
		}
	}
	if match, ok := p.findContainerPublishedPort(ctx, sessionID, workspaceDir, port); ok {
		if !match.LoopbackOnly {
			return false, ProvenanceEvidence{
				Method: ProvenanceExposed, OwnerID: match.Name,
				Description: fmt.Sprintf("container %s publishes port %d beyond loopback", match.Name, port),
			}
		}
		return true, ProvenanceEvidence{
			Method: ProvenanceContainer, OwnerID: match.Name,
			Description: fmt.Sprintf("published by container %s for workspace %s (loopback only)", match.Name, workspaceDir),
		}
	}
	return false, ProvenanceEvidence{
		Method:      ProvenanceUnverified,
		Description: fmt.Sprintf("port %d is not verified as owned by this chat", port),
	}
}

// lineageOwner reports whether every holder of the port descends from an
// action of sessionID, returning one of them.
func (p *DefaultLoopbackProvenance) lineageOwner(sessionID string, held []socketListener) (int, bool) {
	if sessionID == "" || len(held) == 0 || p.lineageSession == nil {
		return 0, false
	}
	for _, l := range held {
		if l.PID <= 0 {
			return 0, false
		}
		if owner, ok := p.lineageSession(l.PID); !ok || owner != sessionID {
			return 0, false
		}
	}
	return held[0].PID, true
}

func onPort(listeners []socketListener, port uint16) []socketListener {
	var out []socketListener
	for _, l := range listeners {
		if l.Port == port {
			out = append(out, l)
		}
	}
	return out
}

// hostListeners reads the kernel socket table where the engine has one, and
// lsof elsewhere.
func hostListeners(ctx context.Context) ([]socketListener, error) {
	table, err := lineage.Listeners()
	if err == nil {
		var out []socketListener
		for _, l := range table {
			if len(l.PIDs) == 0 {
				out = append(out, socketListener{Port: l.Addr.Port(), Addr: l.Addr.Addr()})
			}
			for _, pid := range l.PIDs {
				out = append(out, socketListener{PID: pid, Port: l.Addr.Port(), Addr: l.Addr.Addr()})
			}
		}
		return out, nil
	}
	if !errors.Is(err, lineage.ErrUnsupported) {
		return nil, err
	}
	return lsofListeners(ctx)
}

// lsofListeners exits 1 with no output when nothing listens; any other
// failure leaves the table unknown.
func lsofListeners(ctx context.Context) ([]socketListener, error) {
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	stdout, stderr, code, err := exec.RunSeparate(ctx, lsof,
		[]string{"-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pn"},
		exec.ExecOpts{Launch: exec.HostLaunch("loopback_listener_probe")})
	if err != nil {
		return nil, err
	}
	switch {
	case code == 0:
		return parseLsofListeners(stdout), nil
	case code == 1 && len(bytes.TrimSpace(stdout)) == 0 && len(bytes.TrimSpace(stderr)) == 0:
		return nil, nil
	default:
		return nil, fmt.Errorf("lsof exited %d", code)
	}
}

// parseLsofListeners pairs each name record with the process record before it.
func parseLsofListeners(out []byte) []socketListener {
	var listeners []socketListener
	pid := 0
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = 0
			if v, err := strconv.Atoi(line[1:]); err == nil && v > 0 {
				pid = v
			}
		case 'n':
			if pid == 0 {
				continue
			}
			if l, ok := parseLsofName(line[1:]); ok {
				l.PID = pid
				listeners = append(listeners, l)
			}
		}
	}
	return listeners
}

// parseLsofName reads a numeric listen address: "127.0.0.1:3000",
// "[::1]:3000", or "*:3000" for every interface.
func parseLsofName(name string) (socketListener, bool) {
	idx := strings.LastIndex(name, ":")
	if idx < 0 {
		return socketListener{}, false
	}
	port, err := strconv.ParseUint(name[idx+1:], 10, 16)
	if err != nil || port == 0 {
		return socketListener{}, false
	}
	l := socketListener{Port: uint16(port)}
	host := strings.TrimSuffix(strings.TrimPrefix(name[:idx], "["), "]")
	if host == "*" {
		l.Addr = netip.IPv6Unspecified()
	} else if addr, err := netip.ParseAddr(host); err == nil {
		l.Addr = addr
	}
	return l, true
}

// discoverDockerSockets returns container engine sockets named by the
// environment, the active docker context, and standard runtime paths.
func discoverDockerSockets() []string {
	var sockets []string
	seen := make(map[string]bool)
	addSocket := func(sock string) {
		sock = strings.TrimPrefix(sock, "unix://")
		if sock != "" && !seen[sock] {
			seen[sock] = true
			sockets = append(sockets, sock)
		}
	}
	addSocket(os.Getenv("DOCKER_HOST"))
	addSocket(os.Getenv("CONTAINER_HOST"))
	if home := os.Getenv("HOME"); home != "" {
		addSocket(resolveDockerContextSocket(home))
		for _, c := range []string{
			".colima/default/docker.sock",
			".docker/run/docker.sock",
			".orbstack/run/docker.sock",
			".rd/docker.sock",
			".local/share/containers/podman/machine/podman.sock",
			".local/share/containers/podman/machine/qemu/podman.sock",
			".local/share/containers/podman/machine/applehv/podman.sock",
		} {
			addSocket(filepath.Join(home, c))
		}
		addSocket(fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid()))
	}
	addSocket("/var/run/docker.sock")
	addSocket("/run/podman/podman.sock")
	return sockets
}

func resolveDockerContextSocket(homeDir string) string {
	configData, err := os.ReadFile(filepath.Join(homeDir, ".docker", "config.json")) // #nosec G304 G703 -- fixed user docker config path
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(configData, &cfg); err != nil || cfg.CurrentContext == "" {
		return ""
	}
	// Docker names a context's metadata directory by the digest of its name.
	sum := sha256.Sum256([]byte(cfg.CurrentContext))
	metaDir := filepath.Join(homeDir, ".docker", "contexts", "meta")
	if sock := readSocketFromMetaJSON(filepath.Join(metaDir, hex.EncodeToString(sum[:]), "meta.json")); sock != "" {
		return sock
	}
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sock := readSocketFromMetaJSON(filepath.Join(metaDir, e.Name(), "meta.json")); sock != "" {
			return sock
		}
	}
	return ""
}

func readSocketFromMetaJSON(metaFile string) string {
	data, err := os.ReadFile(filepath.Clean(metaFile)) // #nosec G304 G703 -- context metadata file path
	if err != nil {
		return ""
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err == nil && meta.Endpoints.Docker.Host != "" {
		return strings.TrimPrefix(meta.Endpoints.Docker.Host, "unix://")
	}
	return ""
}

type containerMatchResult struct {
	Name         string
	LoopbackOnly bool
}

type dockerContainerInfo struct {
	ID         string
	Name       string
	WorkingDir string
	Ports      []containerPortMapping
}

type containerPortMapping struct {
	PublicPort   uint16
	LoopbackOnly bool
}

// RecordSessionContainer records an invocation-launched container ID for a session.
func (p *DefaultLoopbackProvenance) RecordSessionContainer(sessionID, containerID, socketPath string) {
	if p == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	containerID = strings.TrimSpace(containerID)
	if sessionID == "" || len(containerID) != 64 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionContainers == nil {
		p.sessionContainers = make(map[string][]string)
	}
	for _, id := range p.sessionContainers[sessionID] {
		if id == containerID {
			return
		}
	}
	p.sessionContainers[sessionID] = append(p.sessionContainers[sessionID], containerID)
}

// ForgetSession clears recorded container provenance for a deleted session.
func (p *DefaultLoopbackProvenance) ForgetSession(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessionContainers, sessionID)
}

func (p *DefaultLoopbackProvenance) sessionContainerIDs(sessionID string) map[string]bool {
	if p == nil || sessionID == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := p.sessionContainers[sessionID]
	if len(ids) == 0 {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// findContainerPublishedPort matches a workspace or session container publishing port. It
// is loopback-only when every publication of that port is.
func (p *DefaultLoopbackProvenance) findContainerPublishedPort(ctx context.Context, sessionID, workspaceDir string, port uint16) (containerMatchResult, bool) {
	if p.containers == nil {
		return containerMatchResult{}, false
	}
	canonWorkspace := ""
	if workspaceDir != "" {
		if c, err := filepath.EvalSymlinks(workspaceDir); err == nil {
			canonWorkspace = c
		} else {
			canonWorkspace = workspaceDir
		}
	}
	recorded := p.sessionContainerIDs(sessionID)
	match, found := containerMatchResult{LoopbackOnly: true}, false
	for _, c := range p.containers(ctx) {
		matched := false
		if recorded[c.ID] {
			matched = true
		} else if canonWorkspace != "" && composeProjectMatches(c.WorkingDir, canonWorkspace, workspaceDir) {
			matched = true
		}
		if !matched {
			continue
		}
		for _, pub := range c.Ports {
			if pub.PublicPort != port {
				continue
			}
			if !found {
				match.Name, found = c.Name, true
			}
			match.LoopbackOnly = match.LoopbackOnly && pub.LoopbackOnly
		}
	}
	return match, found
}

func composeProjectMatches(containerDir, canonWorkspace, origWorkspace string) bool {
	if containerDir == "" {
		return false
	}
	if containerDir == canonWorkspace || containerDir == origWorkspace {
		return true
	}
	canonContainer, err := filepath.EvalSymlinks(containerDir)
	return err == nil && (canonContainer == canonWorkspace || canonContainer == origWorkspace)
}

// queryDockerContainers lists containers from every reachable engine socket.
func queryDockerContainers(ctx context.Context, sockets []string) []dockerContainerInfo {
	var out []dockerContainerInfo
	for _, sock := range sockets {
		if _, err := os.Stat(sock); err != nil {
			continue
		}
		containers, err := queryDockerContainersOverSocket(ctx, sock)
		if err == nil {
			out = append(out, containers...)
		}
	}
	return out
}

func queryDockerContainersOverSocket(ctx context.Context, socketPath string) ([]dockerContainerInfo, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: tr, Timeout: 500 * time.Millisecond}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/v1.41/containers/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	var raw []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Ports []struct {
			IP         string `json:"IP"`
			PublicPort uint16 `json:"PublicPort"`
		} `json:"Ports"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]dockerContainerInfo, 0, len(raw))
	for _, c := range raw {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var mappings []containerPortMapping
		for _, port := range c.Ports {
			if port.PublicPort > 0 {
				mappings = append(mappings, containerPortMapping{
					PublicPort: port.PublicPort, LoopbackOnly: egress.LoopbackLiteral(port.IP),
				})
			}
		}
		out = append(out, dockerContainerInfo{
			ID:         c.ID,
			Name:       name,
			WorkingDir: c.Labels["com.docker.compose.project.working_dir"],
			Ports:      mappings,
		})
	}
	return out, nil
}
