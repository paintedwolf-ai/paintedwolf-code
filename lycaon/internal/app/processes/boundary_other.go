//go:build !darwin

package processes

import "context"

// StartEgress leaves mediation unstarted where command confinement is unavailable.
func (*Runtime) StartEgress(string) error { return nil }

// StartRefusalWatch leaves monitoring unstarted where command confinement is unavailable.
func (*Runtime) StartRefusalWatch(context.Context, string) error { return nil }
