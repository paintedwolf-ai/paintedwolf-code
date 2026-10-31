package session

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/project"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
)

// ProvenanceMethod names the structured fact that decided a port's ownership.
type ProvenanceMethod string

const (
	// ProvenanceLineage: every listener descends from an action of this session.
	ProvenanceLineage ProvenanceMethod = "process_lineage"
	// ProvenanceContainer: the container engine reports the port published by a
	// container whose compose working directory is this workspace.
	ProvenanceContainer ProvenanceMethod = "container_workspace"
	// ProvenanceOtherSession: a listener on the port belongs to another chat's lineage.
	ProvenanceOtherSession ProvenanceMethod = "other_session"
	// ProvenancePreExisting: the port was listening when the engine started.
	ProvenancePreExisting ProvenanceMethod = "pre_existing_host"
	// ProvenanceExposed: a session-owned listener is reachable beyond loopback.
	ProvenanceExposed ProvenanceMethod = "external_exposure"
	// ProvenanceUnverified: no structured fact establishes ownership.
	ProvenanceUnverified ProvenanceMethod = "unverified"
)

// LoopbackHolderKind classifies a single listener holding a loopback port.
type LoopbackHolderKind string

const (
	HolderThisSession        LoopbackHolderKind = "this_session"
	HolderOtherSession       LoopbackHolderKind = "other_session"
	HolderContainerForwarder LoopbackHolderKind = "container_forwarder"
	HolderExternalProcess    LoopbackHolderKind = "external_process"
	HolderUnknown            LoopbackHolderKind = "unknown"
)

// LoopbackHolder describes one process or socket holding a listening port.
type LoopbackHolder struct {
	Kind          LoopbackHolderKind `json:"kind"`
	PID           int                `json:"pid,omitempty"`
	Addr          netip.AddrPort     `json:"addr"`
	IPv6Only      bool               `json:"ipv6_only,omitempty"`
	ProcessName   string             `json:"process_name,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
	SessionTitle  string             `json:"session_title,omitempty"`
	ProjectID     string             `json:"project_id,omitempty"`
	ProjectName   string             `json:"project_name,omitempty"`
	Handle        string             `json:"handle,omitempty"`
	Command       string             `json:"command,omitempty"`
	Background    bool               `json:"background,omitempty"`
	ContainerID   string             `json:"container_id,omitempty"`
	ContainerName string             `json:"container_name,omitempty"`
}

// ContainerPortPublication describes a workspace or session container publishing a port.
type ContainerPortPublication struct {
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      uint16 `json:"host_port,omitempty"`
	LoopbackOnly  bool   `json:"loopback_only"`
	IPv4          bool   `json:"ipv4,omitempty"`
	IPv6          bool   `json:"ipv6,omitempty"`
}

func (c ContainerPortPublication) coversListener(l socketListener) bool {
	if !l.Addr.IsValid() {
		return false
	}
	if l.Addr.Unmap().Is4() {
		return c.IPv4
	}
	if l.Addr.IsUnspecified() && !l.IPv6Only {
		return c.IPv4 || c.IPv6
	}
	return c.IPv6
}

func (c ContainerPortPublication) coversAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return c.IPv4 || c.IPv6
	}
	if addr.Unmap().Is4() {
		return c.IPv4
	}
	return c.IPv6
}

// ProvenanceEvidence describes how session ownership was decided.
type ProvenanceEvidence struct {
	Method             ProvenanceMethod
	OwnerID            string // PID, session ID, or container name
	Description        string
	Holders            []LoopbackHolder
	PublishedContainer *ContainerPortPublication
	ContainerMismatch  bool
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

// LoopbackEndpointInspector inspects the listeners answering a specific loopback address and port.
type LoopbackEndpointInspector interface {
	LoopbackProvenanceResolver
	InspectEndpoint(ctx context.Context, sessionID, workspaceDir string, endpoint netip.AddrPort) (bool, ProvenanceEvidence)
}

// socketListener is one process holding one listening TCP socket. PID is zero
// when the holder cannot be inspected; Addr is invalid when the bind address
// could not be read.
type socketListener struct {
	PID      int
	Port     uint16
	Addr     netip.Addr
	IPv6Only bool
}

// loopbackBound reports whether the listener accepts only local connections.
func (l socketListener) loopbackBound() bool {
	return l.Addr.IsValid() && l.Addr.Unmap().IsLoopback()
}

type lineageInfo struct {
	SessionID string
	LineageID string
	Owner     string
}

type holderSessionMeta struct {
	SessionID     string
	RootSessionID string
	SessionTitle  string
	ProjectID     string
	ProjectName   string
	Handle        string
	Command       string
	Background    bool
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
	// lineageInfo reads the full lineage record for pid when available.
	lineageInfo func(pid int) (lineageInfo, bool)
	// processName reads the kernel executable name for pid.
	processName func(pid int) (string, bool)
	// containers lists running containers from every reachable engine.
	containers func(ctx context.Context) []dockerContainerInfo

	mu                sync.RWMutex
	sessionContainers map[string][]string
	sessions          sessiontree.Reader
	projects          func(ctx context.Context, projectID string) (*project.Project, error)
	processes         func() *bgprocess.Registry
	metaOverride      func(ctx context.Context, sessionID, lineageID, owner string) (holderSessionMeta, bool)
}

// NewLoopbackProvenance snapshots the listening ports present at engine start.
func NewLoopbackProvenance(ctx context.Context) *DefaultLoopbackProvenance {
	sockets := discoverDockerSockets()
	p := newLoopbackProvenance(ctx, hostListeners, lineageSessionOf, func(ctx context.Context) []dockerContainerInfo {
		return queryDockerContainers(ctx, sockets)
	})
	p.lineageInfo = hostLineageInfoOf
	p.processName = osprocess.CommandName
	return p
}

func newLoopbackProvenance(
	parent context.Context,
	listeners func(context.Context) ([]socketListener, error),
	lineageSession func(int) (string, bool),
	containers func(context.Context) []dockerContainerInfo,
) *DefaultLoopbackProvenance {
	p := &DefaultLoopbackProvenance{
		listeners:      listeners,
		lineageSession: lineageSession,
		processName:    osprocess.CommandName,
		containers:     containers,
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if current, err := listeners(ctx); err == nil {
		p.baseline = make(map[uint16]bool, len(current))
		for _, l := range current {
			p.baseline[l.Port] = true
		}
	}
	return p
}

// SetHostSources connects session, project, and background process lookups for holder attribution.
func (p *DefaultLoopbackProvenance) SetHostSources(
	sessions sessiontree.Reader,
	projects func(ctx context.Context, projectID string) (*project.Project, error),
	processes func() *bgprocess.Registry,
) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = sessions
	p.projects = projects
	p.processes = processes
}

func lineageSessionOf(pid int) (string, bool) {
	line, ok := lineage.Of(pid)
	if !ok {
		return "", false
	}
	return line.Session(), true
}

func hostLineageInfoOf(pid int) (lineageInfo, bool) {
	line, ok := lineage.Of(pid)
	if !ok {
		return lineageInfo{}, false
	}
	return lineageInfo{
		SessionID: line.Session(),
		LineageID: string(line.ID()),
		Owner:     line.Owner(),
	}, true
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
	return p.ownership(ctx, sessionID, workspaceDir, port, netip.Addr{}, onPort(current, port))
}

// InspectEndpoint decides whether the listeners answering endpoint belong to the session.
func (p *DefaultLoopbackProvenance) InspectEndpoint(ctx context.Context, sessionID, workspaceDir string, endpoint netip.AddrPort) (bool, ProvenanceEvidence) {
	if p == nil || !endpoint.IsValid() || endpoint.Port() == 0 {
		return false, ProvenanceEvidence{Method: ProvenanceUnverified, Description: "unspecified endpoint or resolver"}
	}
	current, err := p.listeners(ctx)
	if err != nil {
		current = nil
	}
	portListeners := onPort(current, endpoint.Port())
	answering := listenersAnsweringAddr(portListeners, endpoint.Addr())
	return p.ownership(ctx, sessionID, workspaceDir, endpoint.Port(), endpoint.Addr(), answering)
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
	owned, _ := p.ownership(ctx, sessionID, workspaceDir, port, netip.Addr{}, held)
	return !owned
}

// listenersAnsweringAddr selects the listeners on a port that answer connections to target.
// When target is IPv4 (127.0.0.1), an IPv4 listener on 127.0.0.1 or 0.0.0.0 answers directly;
// when no IPv4 listener binds the port, a dual-stack wildcard listener ([::]:port with
// IPV6_V6ONLY=0) answers 127.0.0.1:port. IPv6-only listeners (IPV6_V6ONLY=1) stay scoped to IPv6.
func listenersAnsweringAddr(held []socketListener, target netip.Addr) []socketListener {
	if !target.IsValid() {
		return held
	}
	unmapped := target.Unmap()
	if unmapped.Is4() {
		var v4 []socketListener
		hasV4OnPort := false
		for _, l := range held {
			if !l.Addr.IsValid() {
				continue
			}
			la := l.Addr.Unmap()
			if la.Is4() {
				hasV4OnPort = true
				if la == unmapped || la.IsUnspecified() {
					v4 = append(v4, l)
				}
			}
		}
		if len(v4) > 0 || hasV4OnPort {
			return v4
		}
		var dual []socketListener
		for _, l := range held {
			if l.Addr.IsValid() && l.Addr.Is6() && !l.Addr.Is4In6() && l.Addr.IsUnspecified() && !l.IPv6Only {
				dual = append(dual, l)
			}
		}
		return dual
	}
	var v6 []socketListener
	for _, l := range held {
		if !l.Addr.IsValid() || !l.Addr.Is6() || l.Addr.Is4In6() {
			continue
		}
		if l.Addr == unmapped || l.Addr.IsUnspecified() {
			v6 = append(v6, l)
		}
	}
	return v6
}

func (p *DefaultLoopbackProvenance) ownership(
	ctx context.Context,
	sessionID, workspaceDir string,
	port uint16,
	targetAddr netip.Addr,
	held []socketListener,
) (bool, ProvenanceEvidence) {
	pub, hasPub := p.findContainerPublishedPort(ctx, sessionID, workspaceDir, port)
	var pubPtr *ContainerPortPublication
	if hasPub {
		pubCopy := pub
		pubPtr = &pubCopy
	}

	holders := make([]LoopbackHolder, 0, len(held))
	for _, l := range held {
		holders = append(holders, p.classifyListener(ctx, sessionID, l, pub, hasPub))
	}

	hasForeignOrUnverified := false
	var firstOther *LoopbackHolder
	for i := range holders {
		h := &holders[i]
		if h.Kind == HolderOtherSession && firstOther == nil {
			firstOther = h
		}
		if h.Kind != HolderThisSession && h.Kind != HolderContainerForwarder {
			hasForeignOrUnverified = true
		}
	}
	containerMismatch := hasPub && len(holders) > 0 && hasForeignOrUnverified

	// Any holder in another chat's lineage makes the port held by that other chat,
	// regardless of container publications or baseline state.
	if firstOther != nil {
		ownerID := firstOther.SessionID
		if ownerID == "" && firstOther.PID > 0 {
			ownerID = strconv.Itoa(firstOther.PID)
		}
		return false, ProvenanceEvidence{
			Method:             ProvenanceOtherSession,
			OwnerID:            ownerID,
			Description:        DescribePortHolders(port, holders, pubPtr),
			Holders:            holders,
			PublishedContainer: pubPtr,
			ContainerMismatch:  containerMismatch,
		}
	}

	// A port is owned only when at least one holder exists and every holder is
	// either in this chat's lineage or a verified container forwarder for this chat.
	if len(holders) > 0 && !hasForeignOrUnverified {
		for i, l := range held {
			h := holders[i]
			if h.Kind == HolderThisSession && !l.loopbackBound() {
				return false, ProvenanceEvidence{
					Method:             ProvenanceExposed,
					OwnerID:            strconv.Itoa(l.PID),
					Description:        fmt.Sprintf("listener process %d on port %d is reachable beyond loopback", l.PID, port),
					Holders:            holders,
					PublishedContainer: pubPtr,
				}
			}
			if h.Kind == HolderContainerForwarder && (!pub.LoopbackOnly || !l.loopbackBound()) {
				return false, ProvenanceEvidence{
					Method:             ProvenanceExposed,
					OwnerID:            pub.ContainerName,
					Description:        fmt.Sprintf("container %s publishes port %d beyond loopback", pub.ContainerName, port),
					Holders:            holders,
					PublishedContainer: pubPtr,
				}
			}
		}
		allThisSession := true
		for _, h := range holders {
			if h.Kind != HolderThisSession {
				allThisSession = false
				break
			}
		}
		if allThisSession {
			return true, ProvenanceEvidence{
				Method:             ProvenanceLineage,
				OwnerID:            strconv.Itoa(holders[0].PID),
				Description:        fmt.Sprintf("listener process %d on port %d was launched by session %s", holders[0].PID, port, sessionID),
				Holders:            holders,
				PublishedContainer: pubPtr,
			}
		}
		return true, ProvenanceEvidence{
			Method:             ProvenanceContainer,
			OwnerID:            pub.ContainerName,
			Description:        fmt.Sprintf("published by container %s for workspace %s (loopback only)", pub.ContainerName, workspaceDir),
			Holders:            holders,
			PublishedContainer: pubPtr,
		}
	}

	if p.baseline == nil {
		return false, ProvenanceEvidence{
			Method:             ProvenanceUnverified,
			Description:        fmt.Sprintf("port %d cannot be attributed: the listeners present at start are unknown", port),
			Holders:            holders,
			PublishedContainer: pubPtr,
			ContainerMismatch:  containerMismatch,
		}
	}
	if p.baseline[port] {
		return false, ProvenanceEvidence{
			Method:             ProvenancePreExisting,
			Description:        fmt.Sprintf("port %d was listening before the engine started", port),
			Holders:            holders,
			PublishedContainer: pubPtr,
			ContainerMismatch:  containerMismatch,
		}
	}
	if len(holders) > 0 {
		return false, ProvenanceEvidence{
			Method:             ProvenanceUnverified,
			Description:        DescribePortHolders(port, holders, pubPtr),
			Holders:            holders,
			PublishedContainer: pubPtr,
			ContainerMismatch:  containerMismatch,
		}
	}
	return false, ProvenanceEvidence{
		Method:             ProvenanceUnverified,
		Description:        fmt.Sprintf("port %d is not verified as owned by this chat", port),
		Holders:            holders,
		PublishedContainer: pubPtr,
	}
}

func (p *DefaultLoopbackProvenance) classifyListener(
	ctx context.Context,
	sessionID string,
	l socketListener,
	pub ContainerPortPublication,
	hasPub bool,
) LoopbackHolder {
	var addrPort netip.AddrPort
	if l.Addr.IsValid() {
		addrPort = netip.AddrPortFrom(l.Addr, l.Port)
	}
	if l.PID <= 0 {
		return LoopbackHolder{
			Kind:     HolderUnknown,
			Addr:     addrPort,
			IPv6Only: l.IPv6Only,
		}
	}

	procName := p.resolveProcessName(l.PID)
	if info, ok := p.resolveLineage(l.PID); ok && info.SessionID != "" {
		meta := p.resolveHolderSessionMeta(ctx, info.SessionID, info.LineageID, info.Owner)
		callerRoot := p.rootSessionID(ctx, sessionID)
		sameSession := sessionID != "" && (info.SessionID == sessionID ||
			(callerRoot != "" && (meta.RootSessionID == callerRoot || info.SessionID == callerRoot)))
		kind := HolderOtherSession
		if sameSession {
			kind = HolderThisSession
		}
		targetSessionID := meta.RootSessionID
		if targetSessionID == "" {
			targetSessionID = info.SessionID
		}
		return LoopbackHolder{
			Kind:         kind,
			PID:          l.PID,
			Addr:         addrPort,
			IPv6Only:     l.IPv6Only,
			ProcessName:  procName,
			SessionID:    targetSessionID,
			SessionTitle: meta.SessionTitle,
			ProjectID:    meta.ProjectID,
			ProjectName:  meta.ProjectName,
			Handle:       meta.Handle,
			Command:      meta.Command,
			Background:   meta.Background,
		}
	}

	// Non-lineage process: classify as container_forwarder only when baseline is known,
	// the port was not listening at baseline, a workspace/session container publishes
	// this address family and port, and the process is a known container forwarder.
	if p.baseline != nil && !p.baseline[l.Port] && hasPub && pub.coversListener(l) && isContainerForwarderProcess(procName) {
		return LoopbackHolder{
			Kind:          HolderContainerForwarder,
			PID:           l.PID,
			Addr:          addrPort,
			IPv6Only:      l.IPv6Only,
			ProcessName:   procName,
			ContainerID:   pub.ContainerID,
			ContainerName: pub.ContainerName,
		}
	}

	return LoopbackHolder{
		Kind:        HolderExternalProcess,
		PID:         l.PID,
		Addr:        addrPort,
		IPv6Only:    l.IPv6Only,
		ProcessName: procName,
	}
}

func (p *DefaultLoopbackProvenance) resolveLineage(pid int) (lineageInfo, bool) {
	if p == nil || pid <= 0 {
		return lineageInfo{}, false
	}
	if p.lineageInfo != nil {
		if info, ok := p.lineageInfo(pid); ok && info.SessionID != "" {
			return info, true
		}
	}
	if p.lineageSession != nil {
		if s, ok := p.lineageSession(pid); ok && s != "" {
			return lineageInfo{SessionID: s}, true
		}
	}
	return lineageInfo{}, false
}

func (p *DefaultLoopbackProvenance) resolveProcessName(pid int) string {
	if p == nil || pid <= 0 || p.processName == nil {
		return ""
	}
	name, ok := p.processName(pid)
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

func (p *DefaultLoopbackProvenance) rootSessionID(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	p.mu.RLock()
	sessions := p.sessions
	p.mu.RUnlock()
	if sessions == nil {
		return sessionID
	}
	if root := sessiontree.RootID(ctx, sessions, sessionID); root != "" {
		return root
	}
	return sessionID
}

func (p *DefaultLoopbackProvenance) resolveHolderSessionMeta(ctx context.Context, sessionID, lineageID, ownerSubject string) holderSessionMeta {
	p.mu.RLock()
	override := p.metaOverride
	sessions := p.sessions
	projects := p.projects
	processesFn := p.processes
	p.mu.RUnlock()

	if override != nil {
		if meta, ok := override(ctx, sessionID, lineageID, ownerSubject); ok {
			if meta.RootSessionID == "" {
				meta.RootSessionID = meta.SessionID
			}
			return meta
		}
	}

	meta := holderSessionMeta{
		SessionID:     sessionID,
		RootSessionID: sessionID,
	}
	if processesFn != nil {
		if reg := processesFn(); reg != nil && lineageID != "" {
			if job, ok := reg.LookupByLineage(lineageID); ok {
				if job.RootSessionID != "" {
					meta.RootSessionID = job.RootSessionID
				}
				if job.ProjectID != "" {
					meta.ProjectID = job.ProjectID
				}
				meta.Handle = job.Handle
				meta.Command = job.Command
				meta.Background = job.Background
			}
		}
	}
	if meta.Command == "" && lineageID != "" {
		if cmd, ok := confine.CommandForLineage(lineageID); ok {
			if meta.ProjectID == "" {
				meta.ProjectID = strings.TrimSpace(cmd.ProjectID)
			}
			meta.Command = strings.TrimSpace(cmd.CommandLine)
			if meta.Command == "" {
				meta.Command = strings.TrimSpace(cmd.Image)
			}
		}
	}
	if meta.Command == "" && ownerSubject != "" {
		cleaned := strings.TrimSpace(ownerSubject)
		if rest, ok := strings.CutPrefix(cleaned, "command running "); ok {
			cleaned = strings.TrimSpace(rest)
		}
		meta.Command = cleaned
	}

	if sessions != nil && sessionID != "" {
		rootID := sessiontree.RootID(ctx, sessions, sessionID)
		if rootID != "" {
			meta.RootSessionID = rootID
		}
		if sess, err := sessions.Get(ctx, meta.RootSessionID); err == nil && sess != nil {
			meta.SessionTitle = strings.TrimSpace(sess.Title)
			if meta.ProjectID == "" {
				meta.ProjectID = strings.TrimSpace(sess.ProjectID)
			}
		} else if sess, err := sessions.Get(ctx, sessionID); err == nil && sess != nil {
			meta.SessionTitle = strings.TrimSpace(sess.Title)
			if meta.ProjectID == "" {
				meta.ProjectID = strings.TrimSpace(sess.ProjectID)
			}
		}
	}
	if projects != nil && meta.ProjectID != "" {
		if proj, err := projects(ctx, meta.ProjectID); err == nil && proj != nil {
			meta.ProjectName = strings.TrimSpace(proj.Name)
		}
	}
	return meta
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
	table, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}})
	if err == nil {
		var out []socketListener
		for _, l := range table {
			if len(l.Holders) == 0 {
				out = append(out, socketListener{Port: l.Addr.Port(), Addr: l.Addr.Addr(), IPv6Only: l.IPv6Only})
			}
			for _, h := range l.Holders {
				out = append(out, socketListener{PID: h.PID, Port: l.Addr.Port(), Addr: l.Addr.Addr(), IPv6Only: l.IPv6Only})
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
