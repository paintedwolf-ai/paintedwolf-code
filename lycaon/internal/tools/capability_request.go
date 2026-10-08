package tools

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Direct-IP declaration bounds.
const (
	MaxDirectIPDeclaredDestinations = 16
	MaxDirectIPDeclaredBytes        = 256
)

// DirectIPRequest declares one-action direct outbound access.
type DirectIPRequest struct {
	// DeclaredDestinations are unobserved display and audit context.
	DeclaredDestinations []string
}

// ValidateCapabilityContract checks capabilities against the tool contract.
func ValidateCapabilityContract(contract toolcontract.Contract, args map[string]any) (result *ToolReject) {
	defer func() {
		if result == nil {
			return
		}
		if result.Data == nil {
			result.Data = map[string]any{}
		}
		result.Data["capability_request_fields"] = contract.CapabilityRequestFields()
		result.Data["supports_local_listen"] = contract.Supports(toolcontract.CapabilityLocalListen)
		result.Data["supports_loopback_connect"] = contract.Supports(toolcontract.CapabilityLoopbackConnect)
	}()
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return reject
	}
	unsupported := func(name string) *ToolReject {
		return RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "capability is not declared by the tool contract", "capability": name,
		})
	}
	if request != nil {
		if request.ProcessControl && !contract.Supports(toolcontract.CapabilityProcessControl) {
			return unsupported("process_control")
		}
		if request.HostExecution && !contract.Supports(toolcontract.CapabilityHostExecution) {
			return unsupported("host_execution")
		}
		if len(request.HostResources) > 0 && !contract.Supports(toolcontract.CapabilityHostResource) {
			return unsupported("host_resource")
		}
		if len(request.SocketPaths) > 0 && !contract.Supports(toolcontract.CapabilitySocket) {
			return unsupported("socket")
		}
		if request.DirectIP != nil && !contract.Supports(toolcontract.CapabilityDirectIP) {
			return unsupported("direct_ip")
		}
		if request.LocalListen != nil && !contract.Supports(toolcontract.CapabilityLocalListen) {
			return unsupported("local_listen")
		}
		if request.LoopbackConnect != nil && !contract.Supports(toolcontract.CapabilityLoopbackConnect) {
			return unsupported("loopback_connect")
		}
		if request.WriteRoot != "" && !contract.Supports(toolcontract.CapabilityWriteRoot) {
			return unsupported("write_root")
		}
		if request.ReadPath != "" && !contract.Supports(toolcontract.CapabilityReadPath) {
			return unsupported("read_path")
		}
	}
	if _, present := args["terminal_capture"]; present && !contract.Supports(toolcontract.CapabilityTerminalCapture) {
		return unsupported("terminal_capture")
	}
	return nil
}

// LocalListenRequest is the optional chat-scoped local listener request.
// Declared ports narrow the grant; empty asks for any local port.
type LocalListenRequest struct {
	Ports []uint16
}

// LoopbackConnectRequest declares chat-scoped local outbound access.
type LoopbackConnectRequest struct {
	Ports []uint16
}

// MaxLocalListenPorts bounds capability_request.local_listen.ports.
const MaxLocalListenPorts = 8

// MaxLoopbackConnectPorts bounds capability_request.loopback_connect.ports.
const MaxLoopbackConnectPorts = 8

// CapabilityRequest declares structured execution authority.
type CapabilityRequest struct {
	ProcessControl bool
	HostExecution  bool
	HostResources  []string
	SocketPaths    []string
	// DirectIP preserves an explicit request, including an empty object.
	DirectIP *DirectIPRequest
	// LocalListen is non-nil when the model asked for listener authority (including {}).
	LocalListen *LocalListenRequest
	// LoopbackConnect is non-nil when the model asked to connect to a local service (including {}).
	LoopbackConnect *LoopbackConnectRequest
	// WriteRoot is an explicit absolute root requested before the process starts.
	WriteRoot string
	// ReadPath is an explicit absolute protected path this invocation must read.
	ReadPath string
}

// ParseCapabilityRequest canonicalizes capability_request from tool arguments.
func ParseCapabilityRequest(args map[string]any) (*CapabilityRequest, *ToolReject) {
	if args == nil {
		return nil, nil
	}
	raw, ok := args["capability_request"]
	if !ok || raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "capability_request must be an object",
		})
	}
	if len(obj) == 0 {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "capability_request must not be empty",
		})
	}
	for key := range obj {
		if !toolcontract.IsCapabilityRequestField(key) {
			return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
				"reason": "unsupported capability_request field",
				"field":  key,
			})
		}
	}
	out := &CapabilityRequest{}
	for name, target := range map[string]*bool{"process_control": &out.ProcessControl, "host_execution": &out.HostExecution} {
		if raw, present := obj[name]; present {
			switch value := raw.(type) {
			case bool:
				*target = value
			case map[string]any:
				*target = len(value) == 0
			}
			if !*target {
				return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{"capability": name, "reason": name + " must be true or an empty object"})
			}
		}
	}
	if out.HostExecution && len(obj) != 1 {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{"reason": "host_execution replaces sandbox capabilities; request it alone"})
	}
	if out.HostExecution && boolArg(args, "socks_proxy") {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{"reason": "host_execution does not use the mediated proxy"})
	}

	if resourcesRaw, ok := obj["host_resources"]; ok {
		ids, reject := parseHostResourceIDs(resourcesRaw)
		if reject != nil {
			return nil, reject
		}
		out.HostResources = ids
	}
	if pathsRaw, ok := obj["socket_paths"]; ok {
		paths, reject := parseSocketPaths(pathsRaw)
		if reject != nil {
			return nil, reject
		}
		out.SocketPaths = paths
	}
	if directRaw, ok := obj["direct_ip"]; ok {
		direct, reject := parseDirectIP(directRaw)
		if reject != nil {
			return nil, reject
		}
		out.DirectIP = direct
	}
	if listenRaw, ok := obj["local_listen"]; ok {
		listen, reject := parseLocalListen(listenRaw)
		if reject != nil {
			return nil, reject
		}
		out.LocalListen = listen
	}
	if connectRaw, ok := obj["loopback_connect"]; ok {
		connect, reject := parseLoopbackConnect(connectRaw)
		if reject != nil {
			return nil, reject
		}
		out.LoopbackConnect = connect
	}
	if rootRaw, ok := obj["write_root"]; ok {
		root, reject := parseWriteRoot(rootRaw)
		if reject != nil {
			return nil, reject
		}
		out.WriteRoot = root
	}
	if pathRaw, ok := obj["read_path"]; ok {
		path, reject := parseReadPath(pathRaw)
		if reject != nil {
			return nil, reject
		}
		out.ReadPath = path
	}
	if !out.ProcessControl && !out.HostExecution && len(out.HostResources) == 0 && len(out.SocketPaths) == 0 && out.DirectIP == nil && out.LocalListen == nil && out.LoopbackConnect == nil && out.WriteRoot == "" && out.ReadPath == "" {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "capability_request requires at least one declared capability",
		})
	}
	return out, nil
}

func parseWriteRoot(raw any) (string, *ToolReject) {
	return parseCapabilityPath(raw, "write_root")
}

func parseReadPath(raw any) (string, *ToolReject) {
	return parseCapabilityPath(raw, "read_path")
}

// parseCapabilityPath validates syntax; authority requires the invocation scratch root.
func parseCapabilityPath(raw any, capability string) (string, *ToolReject) {
	path, ok := raw.(string)
	path = filepath.Clean(strings.TrimSpace(path))
	if !ok || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || !utf8.ValidString(path) || len(path) > 1024 {
		return "", RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": capability + " must be one absolute path of at most 1024 bytes", "capability": capability,
		})
	}
	return confine.NormalizeWriteRootKey(path), nil
}

// parseLoopbackConnect accepts {} (any local port) or {ports: [8000, …]}.
func parseLoopbackConnect(raw any) (*LoopbackConnectRequest, *ToolReject) {
	ports, reject := parsePortCapability(raw, "loopback_connect", MaxLoopbackConnectPorts, isolation.CodeLoopbackConnectRequestInvalid)
	if reject != nil {
		return nil, reject
	}
	return &LoopbackConnectRequest{Ports: ports}, nil
}

// parseLocalListen accepts {} (any local port) or {ports: [8000, …]}.
func parseLocalListen(raw any) (*LocalListenRequest, *ToolReject) {
	ports, reject := parsePortCapability(raw, "local_listen", MaxLocalListenPorts, isolation.CodeLocalListenRequestInvalid)
	if reject != nil {
		return nil, reject
	}
	return &LocalListenRequest{Ports: ports}, nil
}

func parsePortCapability(raw any, name string, limit int, code string) ([]uint16, *ToolReject) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, RejectInvalidArguments(code, map[string]any{
			"reason": name + " must be an object",
		})
	}
	for key := range obj {
		if key != "ports" {
			return nil, RejectInvalidArguments(code, map[string]any{
				"reason": "unsupported " + name + " field",
				"field":  key,
			})
		}
	}
	portsRaw, ok := obj["ports"]
	if !ok || portsRaw == nil {
		return nil, nil
	}
	list, ok := portsRaw.([]any)
	if !ok || len(list) == 0 || len(list) > limit {
		return nil, RejectInvalidArguments(code, map[string]any{
			"reason": "ports must be an array of 1 to 8 port numbers",
		})
	}
	seen := map[uint16]struct{}{}
	out := make([]uint16, 0, len(list))
	for _, v := range list {
		port, ok := capabilityPort(v)
		if !ok {
			return nil, RejectInvalidArguments(code, map[string]any{
				"reason": "each port must be an integer between 1 and 65535",
			})
		}
		portNumber := uint16(port) //nolint:gosec // Range checked above.
		if _, dup := seen[portNumber]; dup {
			continue
		}
		seen[portNumber] = struct{}{}
		out = append(out, portNumber)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func capabilityPort(raw any) (int, bool) {
	var port int
	switch value := raw.(type) {
	case int:
		port = value
	case int64:
		port = int(value)
		if int64(port) != value {
			return 0, false
		}
	case float64:
		port = int(value)
		if float64(port) != value {
			return 0, false
		}
	default:
		return 0, false
	}
	return port, port >= 1 && port <= 65535
}

func parseHostResourceIDs(raw any) ([]string, *ToolReject) {
	values, err := stringList(raw)
	if err != nil || len(values) == 0 || len(values) > hostresources.RequirementsMax {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "host_resources must contain 1 to 16 ids",
		})
	}
	ids, parseErr := hostresources.ParseRequirements(strings.Join(values, ","))
	if parseErr != nil {
		return nil, RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": parseErr.Error(),
		})
	}
	return ids, nil
}

func parseSocketPaths(pathsRaw any) ([]string, *ToolReject) {
	paths, err := stringList(pathsRaw)
	if err != nil {
		return nil, RejectInvalidArguments(isolation.CodeSocketPathInvalid, map[string]any{
			"reason": "socket_paths must be an array of strings",
		})
	}
	if len(paths) == 0 {
		return nil, RejectInvalidArguments(isolation.CodeSocketPathInvalid, map[string]any{
			"reason": "socket_paths must contain 1 to 8 paths",
		})
	}
	if len(paths) > confine.MaxSocketGrants {
		return nil, RejectInvalidArguments(isolation.CodeSocketPathLimit, map[string]any{
			"limit": confine.MaxSocketGrants,
			"count": len(paths),
		})
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsRune(p, 0) || !utf8.ValidString(p) {
			return nil, RejectInvalidArguments(isolation.CodeSocketPathInvalid, map[string]any{
				"reason": "socket path must be non-empty valid UTF-8 without NUL",
				"path":   p,
			})
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, RejectInvalidArguments(isolation.CodeSocketPathInvalid, map[string]any{
			"reason": "socket_paths must contain 1 to 8 paths",
		})
	}
	return out, nil
}

func parseDirectIP(raw any) (*DirectIPRequest, *ToolReject) {
	switch v := raw.(type) {
	case bool:
		if !v {
			return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
				"reason": "direct_ip: false is not a request — omit the field",
			})
		}
		return &DirectIPRequest{}, nil
	case []any:
		return parseDirectIPObject(map[string]any{"declared_destinations": v})
	case []string:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = item
		}
		return parseDirectIPObject(map[string]any{"declared_destinations": items})
	case map[string]any:
		return parseDirectIPObject(v)
	default:
		return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
			"reason": "direct_ip must be true, a destination list, or an object",
		})
	}
}

func parseDirectIPObject(obj map[string]any) (*DirectIPRequest, *ToolReject) {
	for key := range obj {
		if key != "declared_destinations" {
			return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
				"reason": "unsupported direct_ip field",
				"field":  key,
			})
		}
	}
	req := &DirectIPRequest{}
	destRaw, ok := obj["declared_destinations"]
	if !ok || destRaw == nil {
		return req, nil
	}
	dests, err := stringList(destRaw)
	if err != nil {
		return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
			"reason": "declared_destinations must be an array of strings",
		})
	}
	if len(dests) > MaxDirectIPDeclaredDestinations {
		return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
			"reason": "declared_destinations exceeds max",
			"limit":  MaxDirectIPDeclaredDestinations,
			"count":  len(dests),
		})
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dests))
	for _, d := range dests {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !utf8.ValidString(d) || hasNULOrControl(d) {
			return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
				"reason": "declared destination must be valid UTF-8 without NUL or control characters",
			})
		}
		if utf8.RuneCountInString(d) == 0 {
			continue
		}
		if len(d) > MaxDirectIPDeclaredBytes {
			return nil, RejectInvalidArguments(isolation.CodeDirectIPRequestInvalid, map[string]any{
				"reason": "declared destination exceeds max byte length",
				"limit":  MaxDirectIPDeclaredBytes,
			})
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	req.DeclaredDestinations = out
	return req, nil
}

func hasNULOrControl(s string) bool {
	for _, r := range s {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func stringList(v any) ([]string, error) {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...), nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, errNotStringList
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, errNotStringList
	}
}

var errNotStringList = errString("not a string list")

type errString string

func (e errString) Error() string { return string(e) }

// parseSocksProxyArg validates the top-level proxy-environment switch.
func parseSocksProxyArg(args map[string]any) (bool, *ToolReject) {
	if args == nil {
		return false, nil
	}
	raw, ok := args["socks_proxy"]
	if !ok || raw == nil {
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, RejectInvalidArguments(isolation.CodeSocksProxyInvalid, map[string]any{
			"reason": "socks_proxy must be a boolean",
		})
	}
	return b, nil
}

// ResolveCapabilitySockets resolves every requested path. Failures reject before spawn.
func ResolveCapabilitySockets(req *CapabilityRequest) ([]confine.SocketGrant, *ToolReject) {
	if req == nil || len(req.SocketPaths) == 0 {
		return nil, nil
	}
	out := make([]confine.SocketGrant, 0, len(req.SocketPaths))
	byResolved := map[string]confine.SocketGrant{}
	for _, path := range req.SocketPaths {
		g, err := confine.ResolveSocketRequest(path)
		if err != nil {
			return nil, socketResolveReject(err)
		}
		if _, ok := byResolved[g.ResolvedPath]; ok {
			continue
		}
		byResolved[g.ResolvedPath] = g
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResolvedPath == out[j].ResolvedPath {
			return out[i].ApprovedPath < out[j].ApprovedPath
		}
		return out[i].ResolvedPath < out[j].ResolvedPath
	})
	return out, nil
}

func socketResolveReject(err error) *ToolReject {
	var se *confine.SocketResolveError
	if errors.As(err, &se) && se != nil {
		code := isolation.CodeSocketPathRefused
		switch se.Code {
		case confine.SocketResolveInvalid:
			code = isolation.CodeSocketPathInvalid
		case confine.SocketResolveNotFound:
			code = isolation.CodeSocketPathNotFound
		case confine.SocketResolveNotSocket:
			code = isolation.CodeSocketPathNotSocket
		case confine.SocketResolveLimit:
			code = isolation.CodeSocketPathLimit
		case confine.SocketResolveChanged:
			code = isolation.CodeSocketPathChanged
		default:
		}
		return RejectInvalidArguments(code, map[string]any{"path": se.Path, "reason": se.Error()})
	}
	return &ToolReject{Code: isolation.CodeSocketPathRefused, Data: map[string]any{
		"reason": err.Error(),
	}}
}
