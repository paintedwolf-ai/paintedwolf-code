package confine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type actionLaunchIdentity struct {
	boundary *Confinement
	facts    [32]byte
}

func actionLaunchDigest(c *Confinement, command EgressCommand) ([32]byte, error) {
	data, err := json.Marshal(struct {
		Boundary *Confinement
		Action   EgressCommand
	}{c, command})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (l *ActionLease) captureLaunch(c *Confinement) error {
	digest, err := actionLaunchDigest(c, l.cmd)
	if err != nil {
		return err
	}
	l.launch = actionLaunchIdentity{c, digest}
	return nil
}

// ValidateLaunch rechecks the actual admitted action without consuming permits
// again. Metadata capture under this execution capability grants no source read.
func (l *ActionLease) ValidateLaunch(ctx context.Context, c *Confinement, current EgressCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.closed.Load() || l.launch.boundary != c {
		return fmt.Errorf("launch action is not live")
	}
	digest, err := actionLaunchDigest(c, current)
	if err != nil {
		return err
	}
	if digest != l.launch.facts {
		return fmt.Errorf("launch action or boundary changed")
	}
	if c != nil && c.Network == NetworkProxyOnly && !EgressBound(c) {
		return fmt.Errorf("launch egress lease is not live")
	}
	return nil
}
