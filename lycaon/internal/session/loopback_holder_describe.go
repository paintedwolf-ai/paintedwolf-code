package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/pkg/api"
)

// isContainerForwarderProcess reports whether procName is a known container engine
// port-forwarding process.
func isContainerForwarderProcess(procName string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(procName)))
	switch base {
	case "docker-proxy", "dockerd",
		"com.docker.backend", "com.docker.backe",
		"com.docker.vpnkit", "com.docker.vpnki", "vpnkit-bridge",
		"gvproxy", "vfkit",
		"orbstack", "orbstack helper",
		"limactl", "colima",
		"podman", "rootlessport", "slirp4netns", "pasta", "conmon":
		return true
	default:
		return false
	}
}

// DescribePortHolders formats a human-readable summary of all holders on port,
// including container publication mismatches when present.
func DescribePortHolders(port uint16, holders []LoopbackHolder, pub *ContainerPortPublication) string {
	if len(holders) == 0 {
		if pub != nil && pub.ContainerName != "" {
			return fmt.Sprintf("Container %s in this chat publishes port %d, but no host process is listening on it.", pub.ContainerName, port)
		}
		return fmt.Sprintf("Port %d has no active listener.", port)
	}
	multi := len(holders) > 1
	parts := make([]string, 0, len(holders)+1)
	hasContainerHolder := false
	for _, h := range holders {
		if h.Kind == HolderContainerForwarder {
			hasContainerHolder = true
		}
		parts = append(parts, describeSingleHolder(port, h, multi))
	}
	if pub != nil && pub.ContainerName != "" && !hasContainerHolder {
		family := ""
		switch {
		case pub.IPv4 && !pub.IPv6:
			family = " on IPv4 (127.0.0.1)"
		case pub.IPv6 && !pub.IPv4:
			family = " on IPv6 ([::1])"
		}
		parts = append(parts, fmt.Sprintf("Container %s in this chat publishes port %d%s.", pub.ContainerName, port, family))
	}
	return strings.Join(parts, " ")
}

func describeSingleHolder(port uint16, h LoopbackHolder, includeAddr bool) string {
	loc := fmt.Sprintf("Port %d", port)
	if includeAddr && h.Addr.IsValid() {
		loc = fmt.Sprintf("Port %d (%s)", port, h.Addr.Addr())
	}
	switch h.Kind {
	case HolderOtherSession:
		cmdPart := "a process"
		if h.Command != "" {
			cmdPart = "`" + h.Command + "`"
		} else if h.ProcessName != "" {
			cmdPart = "`" + h.ProcessName + "`"
		}
		rolePart := ""
		if h.Background {
			if h.Handle != "" {
				rolePart = fmt.Sprintf(", a background command (%s)", h.Handle)
			} else {
				rolePart = ", a background command"
			}
		} else if h.Handle != "" {
			rolePart = fmt.Sprintf(" (%s)", h.Handle)
		}
		chatPart := " in another chat"
		if h.SessionTitle != "" {
			chatPart = fmt.Sprintf(" in the chat '%s'", h.SessionTitle)
		} else if h.SessionID != "" {
			chatPart = fmt.Sprintf(" in chat %s", h.SessionID)
		}
		projPart := ""
		if h.ProjectName != "" {
			projPart = fmt.Sprintf(" in the project '%s'", h.ProjectName)
		} else if h.ProjectID != "" {
			projPart = fmt.Sprintf(" in project %s", h.ProjectID)
		}
		return fmt.Sprintf("%s is served by %s%s%s%s.", loc, cmdPart, rolePart, chatPart, projPart)
	case HolderExternalProcess:
		if h.ProcessName != "" && h.PID > 0 {
			return fmt.Sprintf("%s is held by external process `%s` (PID %d) running outside Painted Wolf Code.", loc, h.ProcessName, h.PID)
		}
		if h.PID > 0 {
			return fmt.Sprintf("%s is held by external process PID %d running outside Painted Wolf Code.", loc, h.PID)
		}
		return fmt.Sprintf("%s is held by an external process running outside Painted Wolf Code.", loc)
	case HolderUnknown:
		return fmt.Sprintf("%s is held by an uninspectable host process.", loc)
	case HolderContainerForwarder:
		if h.ContainerName != "" {
			return fmt.Sprintf("%s is forwarded for container %s in this chat.", loc, h.ContainerName)
		}
		return fmt.Sprintf("%s is forwarded for a container in this chat.", loc)
	case HolderThisSession:
		if h.Command != "" {
			return fmt.Sprintf("%s is served by `%s` in this chat.", loc, h.Command)
		}
		return fmt.Sprintf("%s is served by process %d in this chat.", loc, h.PID)
	default:
		return fmt.Sprintf("%s has an unverified listener.", loc)
	}
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

type dockerContainerInfo struct {
	ID         string
	Name       string
	WorkingDir string
	Ports      []containerPortMapping
}

type containerPortMapping struct {
	IP           string
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
func (p *DefaultLoopbackProvenance) findContainerPublishedPort(ctx context.Context, sessionID, workspaceDir string, port uint16) (ContainerPortPublication, bool) {
	if p.containers == nil {
		return ContainerPortPublication{}, false
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
	if rootID := p.rootSessionID(ctx, sessionID); rootID != "" && rootID != sessionID {
		for id := range p.sessionContainerIDs(rootID) {
			if recorded == nil {
				recorded = make(map[string]bool)
			}
			recorded[id] = true
		}
	}
	match, found := ContainerPortPublication{LoopbackOnly: true}, false
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
				match.ContainerID = c.ID
				match.ContainerName = c.Name
				match.HostIP = strings.TrimSpace(pub.IP)
				match.HostPort = pub.PublicPort
				found = true
			}
			match.LoopbackOnly = match.LoopbackOnly && pub.LoopbackOnly
			ip := strings.TrimSpace(pub.IP)
			if ip == "" {
				match.IPv4 = true
				match.IPv6 = true
			} else if addr, err := netip.ParseAddr(ip); err == nil {
				if addr.Unmap().Is4() {
					match.IPv4 = true
				} else {
					match.IPv6 = true
				}
			} else {
				match.IPv4 = true
				match.IPv6 = true
			}
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
					IP:           port.IP,
					PublicPort:   port.PublicPort,
					LoopbackOnly: egress.LoopbackLiteral(port.IP),
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

func applyPortHolderDetails(
	ctx context.Context,
	provenance LoopbackProvenanceResolver,
	invokingSessionID, projectDir string,
	ports []uint16,
	details map[string]any,
) string {
	if provenance == nil || len(ports) == 0 {
		return ""
	}
	var allHolders []LoopbackHolder
	var published *ContainerPortPublication
	var mismatch bool
	var descriptions []string
	for _, port := range ports {
		_, ev := provenance.IsSessionOwned(ctx, invokingSessionID, projectDir, port)
		if len(ev.Holders) > 0 {
			allHolders = append(allHolders, ev.Holders...)
		}
		if ev.ContainerMismatch {
			mismatch = true
		}
		if ev.PublishedContainer != nil && published == nil {
			cp := *ev.PublishedContainer
			published = &cp
		}
		if len(ev.Holders) > 0 || (ev.ContainerMismatch && ev.PublishedContainer != nil) {
			desc := DescribePortHolders(port, ev.Holders, ev.PublishedContainer)
			if desc == "" {
				desc = strings.TrimSpace(ev.Description)
			}
			if desc != "" && !slices.Contains(descriptions, desc) {
				descriptions = append(descriptions, desc)
			}
		}
	}
	if details != nil {
		if len(allHolders) > 0 {
			holderMaps := make([]map[string]any, 0, len(allHolders))
			for _, h := range allHolders {
				m := map[string]any{
					"kind": string(h.Kind),
				}
				if h.Addr.IsValid() {
					m["address"] = h.Addr.String()
				}
				if h.PID > 0 {
					m["pid"] = h.PID
				}
				if h.ProcessName != "" {
					m["process_name"] = h.ProcessName
				}
				if h.SessionID != "" {
					m["session_id"] = h.SessionID
				}
				if h.SessionTitle != "" {
					m["session_title"] = h.SessionTitle
				}
				if h.ProjectID != "" {
					m["project_id"] = h.ProjectID
				}
				if h.ProjectName != "" {
					m["project_name"] = h.ProjectName
				}
				if h.Handle != "" {
					m["handle"] = h.Handle
				}
				if h.Command != "" {
					m["command"] = h.Command
				}
				if h.Background {
					m["background"] = true
				}
				if h.ContainerID != "" {
					m["container_id"] = h.ContainerID
				}
				if h.ContainerName != "" {
					m["container_name"] = h.ContainerName
				}
				holderMaps = append(holderMaps, m)
			}
			details["holders"] = holderMaps
		}
		if mismatch {
			details["container_mismatch"] = true
		}
		if published != nil && (len(allHolders) > 0 || mismatch) {
			pubMap := map[string]any{
				"loopback_only": published.LoopbackOnly,
			}
			if published.ContainerID != "" {
				pubMap["container_id"] = published.ContainerID
			}
			if published.ContainerName != "" {
				pubMap["container_name"] = published.ContainerName
			}
			if published.HostIP != "" {
				pubMap["host_ip"] = published.HostIP
			}
			if published.HostPort > 0 {
				pubMap["host_port"] = int(published.HostPort)
			}
			details["published_container"] = pubMap
		}
	}
	return strings.Join(descriptions, " ")
}

// ObserveLoopbackEndpoint implements tools.LoopbackEndpointObserver.
func (p *DefaultLoopbackProvenance) ObserveLoopbackEndpoint(
	ctx context.Context,
	sessionID, workspaceDir string,
	peer netip.AddrPort,
) *api.LoopbackListenerFact {
	if p == nil || !peer.IsValid() {
		return nil
	}
	_, ev := p.InspectEndpoint(ctx, sessionID, workspaceDir, peer)
	if len(ev.Holders) == 0 && ev.PublishedContainer == nil {
		return nil
	}
	holders := make([]api.LoopbackListenerHolder, 0, len(ev.Holders))
	for _, h := range ev.Holders {
		var addr string
		if h.Addr.IsValid() {
			addr = h.Addr.String()
		}
		holders = append(holders, api.LoopbackListenerHolder{
			Kind:          string(h.Kind),
			Address:       addr,
			PID:           h.PID,
			ProcessName:   h.ProcessName,
			SessionID:     h.SessionID,
			SessionTitle:  h.SessionTitle,
			ProjectID:     h.ProjectID,
			ProjectName:   h.ProjectName,
			Handle:        h.Handle,
			Command:       h.Command,
			Background:    h.Background,
			ContainerID:   h.ContainerID,
			ContainerName: h.ContainerName,
		})
	}
	fact := &api.LoopbackListenerFact{
		ConnectedAddress:  peer.String(),
		Holders:           holders,
		ContainerMismatch: ev.ContainerMismatch,
		Description:       strings.TrimSpace(ev.Description),
	}
	if ev.PublishedContainer != nil {
		fact.PublishedContainer = &api.LoopbackPublishedContainer{
			ContainerID:   ev.PublishedContainer.ContainerID,
			ContainerName: ev.PublishedContainer.ContainerName,
			HostIP:        ev.PublishedContainer.HostIP,
			HostPort:      int(ev.PublishedContainer.HostPort),
			LoopbackOnly:  ev.PublishedContainer.LoopbackOnly,
		}
	}
	if len(holders) == 1 {
		h := holders[0]
		fact.Kind = h.Kind
		fact.Address = h.Address
		fact.PID = h.PID
		fact.ProcessName = h.ProcessName
		fact.SessionID = h.SessionID
		fact.SessionTitle = h.SessionTitle
		fact.ProjectID = h.ProjectID
		fact.ProjectName = h.ProjectName
		fact.Handle = h.Handle
		fact.Command = h.Command
		fact.Background = h.Background
		fact.ContainerID = h.ContainerID
		fact.ContainerName = h.ContainerName
	} else if len(holders) > 1 {
		fact.Ambiguous = true
	}
	return fact
}
