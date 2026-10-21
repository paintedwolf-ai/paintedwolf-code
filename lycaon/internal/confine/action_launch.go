package confine

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

type actionLaunchIdentity struct {
	boundary *Confinement
	snapshot *Confinement
}

func cloneEgressCommand(command EgressCommand) EgressCommand {
	command.DeclaredHosts = slices.Clone(command.DeclaredHosts)
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
	frozen.SocketGrants = slices.Clone(c.SocketGrants)
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
	return nil
}
