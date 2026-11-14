package confine

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/socketref"
)

type actionLaunchIdentity struct {
	boundary *Confinement
	snapshot *Confinement
}

func cloneEgressCommand(command EgressCommand) EgressCommand {
	command.DeclaredHosts = slices.Clone(command.DeclaredHosts)
	command.OwnedLoopbackPorts = slices.Clone(command.OwnedLoopbackPorts)
	if command.UserRule != nil {
		rule := *command.UserRule
		command.UserRule = &rule
	}
	return command
}

func (l *ActionLease) captureLaunch(c *Confinement) {
	l.launch.boundary = c
	if c == nil {
		return
	}
	frozen := *c
	frozen.Roots = slices.Clone(c.Roots)
	frozen.GrantedWriteRoots = slices.Clone(c.GrantedWriteRoots)
	frozen.ReadRoots = slices.Clone(c.ReadRoots)
	frozen.ReadDenyPaths = slices.Clone(c.ReadDenyPaths)
	frozen.View.DeniedPaths = slices.Clone(c.View.DeniedPaths)
	frozen.View.ReopenedPaths = slices.Clone(c.View.ReopenedPaths)
	frozen.View.ReopenedReadPaths = slices.Clone(c.View.ReopenedReadPaths)
	frozen.View.DeniedLoopbackPorts = slices.Clone(c.View.DeniedLoopbackPorts)
	frozen.SocketGrants = slices.Clone(c.SocketGrants)
	frozen.HostServicePaths = slices.Clone(c.HostServicePaths)
	frozen.socketEndpoints = maps.Clone(c.socketEndpoints)
	frozen.DroppedSocketGrants = slices.Clone(c.DroppedSocketGrants)
	frozen.ProtectedWriteGrants = slices.Clone(c.ProtectedWriteGrants)
	frozen.PolicyWriteGrants = slices.Clone(c.PolicyWriteGrants)
	frozen.DroppedProtectedWriteGrants = slices.Clone(c.DroppedProtectedWriteGrants)
	frozen.ProtectedReadGrants = slices.Clone(c.ProtectedReadGrants)
	frozen.DroppedProtectedReadGrants = slices.Clone(c.DroppedProtectedReadGrants)
	frozen.DirectIPPermits = slices.Clone(c.DirectIPPermits)
	frozen.LocalListenPorts = slices.Clone(c.LocalListenPorts)
	frozen.LoopbackConnectPorts = slices.Clone(c.LoopbackConnectPorts)
	l.launch.snapshot = &frozen
}

// ValidateLaunch rechecks the actual admitted action without consuming permits
// again. Exact typed values preserve raw native pathname/argv bytes; display
// encodings are never authority identities. Lineage remains the same live handle.
func (l *ActionLease) ValidateLaunch(ctx context.Context, c *Confinement, current EgressCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.closed.Load() || l.launch.boundary != c {
		return fmt.Errorf("launch action is not live")
	}
	if !reflect.DeepEqual(l.launch.snapshot, c) || !reflect.DeepEqual(l.cmd, current) {
		return fmt.Errorf("launch action or boundary changed")
	}
	if c != nil && c.Network == NetworkProxyOnly && !EgressBound(c) {
		return fmt.Errorf("launch egress lease is not live")
	}
	return c.SocketError()
}

func (l *ActionLease) bindSockets(c *Confinement) error {
	if len(c.SocketGrants) == 0 {
		return nil
	}
	endpoints := make(map[string]string, 2*len(c.SocketGrants))
	for _, grant := range c.SocketGrants {
		owner, err := grant.Binding.Acquire()
		if err != nil {
			return &SocketResolveError{Code: SocketResolveChanged, Path: grant.ApprovedPath, Err: err}
		}
		l.socketOwners = append(l.socketOwners, owner)
		for _, network := range []string{"unix", "unixgram"} {
			path := grant.ResolvedPath
			if runtime.GOOS == "darwin" {
				path, err = owner.Endpoint(network)
				if err != nil {
					return &SocketResolveError{Code: SocketResolveRefused, Path: grant.ApprovedPath, Err: fmt.Errorf("prepare retained service endpoint: %w", err)}
				}
			}
			endpoints[socketEndpointKey(grant.Binding, network)] = path
		}
	}
	c.socketEndpoints = endpoints
	return nil
}

// HostService is a socket the host serves to one action, such as the
// credential service an agent's Git asks. Unlike a user's socket grant it is
// never relayed: the service must observe the confined client as its peer.
type HostService struct {
	// path is admitted by an exact kernel path rule.
	path string
	// grant is the retained object a connect mediator dials instead.
	grant *SocketGrant
}

// ServeHostSocket admits path, a socket the host listens on inside a
// socketref.ServiceDirectory, to one action; release ends the admission.
// A kernel path rule is safe there because every write floor denies that
// directory, so no action can repoint the path. A connect mediator instead
// dials the retained socket, and the client's identity must reach the
// service in band.
func ServeHostSocket(path string) (HostService, func(), error) {
	switch socketref.Platform().Service {
	case socketref.KernelPathname:
		canonical := fspath.CanonicalPath(path)
		if canonical == "" || !socketref.IsServicePath(canonical) {
			return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: fmt.Errorf("not inside a host service directory")}
		}
		if info, err := os.Lstat(canonical); err != nil || info.Mode()&os.ModeSocket == 0 {
			return HostService{}, nil, &SocketResolveError{Code: SocketResolveNotSocket, Path: path, Err: err}
		}
		return HostService{path: canonical}, func() {}, nil
	case socketref.RetainedObject:
		lease, err := socketref.Capture(path)
		if err != nil {
			return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: err}
		}
		grant := SocketGrant{ApprovedPath: path, ResolvedPath: fspath.CanonicalPath(path), Binding: lease.Binding()}
		return HostService{grant: &grant}, lease.Close, nil
	default:
		return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: socketref.ErrUnsupported}
	}
}

func hostServiceAdmissions(services []HostService) (grants []SocketGrant, paths []string) {
	for _, service := range services {
		switch {
		case service.grant != nil:
			grants = append(grants, *service.grant)
		case service.path != "":
			paths = append(paths, service.path)
		}
	}
	return grants, paths
}

func (l *ActionLease) releaseSockets() {
	for _, owner := range l.socketOwners {
		owner.Close()
	}
	l.socketOwners = nil
}

// SocketError reports unavailable retained authority without converting a host
// refusal into a kernel sandbox observation.
func (c *Confinement) SocketError() error {
	if c == nil {
		return nil
	}
	for _, grant := range c.SocketGrants {
		owner, err := grant.Binding.Acquire()
		if err != nil {
			return &SocketResolveError{Code: SocketResolveChanged, Path: grant.ApprovedPath, Err: err}
		}
		owner.Close()
	}
	return nil
}

const socketReferencePrefix = "{{paintedwolf-socket:"

func socketEndpointKey(binding socketref.Binding, network string) string {
	return binding.Key() + "\x00" + network
}

// ResolveSocketArguments expands declared endpoint references after shell
// evaluation and before the final argv/environment are captured for exec.
// Ordinary path bytes never trigger rewriting.
func ResolveSocketArguments(c *Confinement, args, environment []string) ([]string, []string, error) {
	resolve := func(value string) (string, error) {
		if !strings.Contains(value, socketReferencePrefix) {
			return value, nil
		}
		if !strings.HasPrefix(value, socketReferencePrefix) || !strings.HasSuffix(value, "}}") {
			return "", &SocketResolveError{Code: SocketResolveInvalid, Err: fmt.Errorf("a socket reference must occupy an entire argument or environment value")}
		}
		encoded := strings.TrimSuffix(strings.TrimPrefix(value, socketReferencePrefix), "}}")
		network := "unix"
		prefix := ""
		if rest, unixURL := strings.CutPrefix(encoded, "unix-url:"); unixURL {
			encoded, prefix = rest, "unix://"
		} else if rest, datagram := strings.CutPrefix(encoded, "unixgram:"); datagram {
			network, encoded = "unixgram", rest
		}
		path, err := url.PathUnescape(encoded)
		if err != nil || socketPathSyntaxReason(path) != "" {
			return "", &SocketResolveError{Code: SocketResolveInvalid, Path: path, Err: err}
		}
		if c != nil {
			for _, grant := range c.SocketGrants {
				if path != grant.ApprovedPath && path != grant.ResolvedPath {
					continue
				}
				if err := c.SocketError(); err != nil {
					return "", err
				}
				if endpoint := c.socketEndpoints[socketEndpointKey(grant.Binding, network)]; endpoint != "" {
					return prefix + endpoint, nil
				}
			}
		}
		return "", &SocketResolveError{Code: SocketResolveChanged, Path: path, Err: socketref.ErrUnbound}
	}
	resolvedArgs, resolvedEnv := slices.Clone(args), slices.Clone(environment)
	for i, arg := range args {
		var err error
		if resolvedArgs[i], err = resolve(arg); err != nil {
			return nil, nil, err
		}
	}
	for i, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		resolved, err := resolve(value)
		if err != nil {
			return nil, nil, err
		}
		resolvedEnv[i] = key + "=" + resolved
	}
	return resolvedArgs, resolvedEnv, nil
}

// SocketEndpointReferences formats declaration references without granting access.
func SocketEndpointReferences(path string) (string, string) {
	if socketPathSyntaxReason(path) != "" {
		return "", ""
	}
	encoded := strings.ReplaceAll(url.QueryEscape(path), "+", "%20")
	encoded = strings.ReplaceAll(encoded, "%2F", "/")
	return socketReferencePrefix + encoded + "}}", socketReferencePrefix + "unix-url:" + encoded + "}}"
}
