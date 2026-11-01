//go:build linux

package confine

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/socketref"
)

func prepareCredentialSocket(c Confinement) (Confinement, func(), error) {
	if !c.CredentialStore {
		return c, func() {}, nil
	}
	path, ok := ResolveCredentialStoreDBusSocket(os.LookupEnv, os.Getuid())
	if !ok {
		return c, func() {}, fmt.Errorf("OS credential-store session bus is unavailable")
	}
	lease, err := socketref.Capture(path)
	if err != nil {
		return c, func() {}, err
	}
	// Preserve explicit selectors but prevent an alias of this same retained
	// bus from selecting a raw connection under a credential read approval.
	c.SocketGrants = append([]SocketGrant(nil), c.SocketGrants...)
	found := false
	for i := range c.SocketGrants {
		if c.SocketGrants[i].Binding.SameObject(lease.Binding()) {
			c.SocketGrants[i].credentialRead = true
			found = found || c.SocketGrants[i].ResolvedPath == path
		}
	}
	if !found {
		c.SocketGrants = append(c.SocketGrants, SocketGrant{ApprovedPath: path, ResolvedPath: path, Binding: lease.Binding(), credentialRead: true})
	}
	if len(c.SocketGrants) > MaxSocketGrants {
		lease.Close()
		return c, func() {}, ErrSocketGrantCap
	}
	return c, lease.Close, nil
}

func socketCommandFiles(grants []SocketGrant) ([]*os.File, func(), error) {
	files := make([]*os.File, 0, len(grants))
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, grant := range grants {
		lease, err := grant.Binding.Acquire()
		if err != nil {
			closeFiles()
			return nil, func() {}, err
		}
		file, err := lease.Duplicate()
		lease.Close()
		if err != nil {
			closeFiles()
			return nil, func() {}, err
		}
		files = append(files, file)
	}
	return files, closeFiles, nil
}

func appendSocketCommandFiles(extra []*os.File, files []*os.File) ([]*os.File, error) {
	if len(files) == 0 {
		return extra, nil
	}
	if len(extra)+helperProfileFD > linuxsandbox.SocketReferenceFD {
		return nil, fmt.Errorf("socket reference descriptors overlap helper control")
	}
	for len(extra)+helperProfileFD < linuxsandbox.SocketReferenceFD {
		extra = append(extra, nil)
	}
	return append(extra, files...), nil
}

// delegatedEndpoints are the endpoints whose sockets the plan marks delegated.
var delegatedEndpoints = linuxsandbox.KnownDelegatedEndpoints()

// socketPlan names each retained destination's descriptor and marks one whose
// object is a delegated service's endpoint socket. The comparison reads the
// retained object and the fixed endpoint; the grant's own name is never
// looked up again.
func socketPlan(grants []SocketGrant) []linuxsandbox.SocketGrant {
	planned := make([]linuxsandbox.SocketGrant, 0, len(grants))
	for i, grant := range grants {
		next := linuxsandbox.SocketGrant{Path: grant.ResolvedPath, FD: linuxsandbox.SocketReferenceFD + i, CredentialRead: grant.credentialRead}
		for _, endpoint := range delegatedEndpoints {
			if grant.ResolvedPath == endpoint.Path || grant.Binding.MatchesPath(endpoint.Path) {
				next.Delegated = endpoint.Service
				break
			}
		}
		planned = append(planned, next)
	}
	return planned
}
