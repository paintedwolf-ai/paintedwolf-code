package confine

import "strings"

// SandboxRefusal is one operation the kernel refused a process of an action.
// Repeats of one operation on one target fold into Count.
type SandboxRefusal struct {
	// Operation is the kernel's name for the refused operation.
	Operation string `json:"operation"`
	// Target is the path, socket, or port the kernel named; empty when it named none.
	Target string `json:"target,omitempty"`
	// Process is the executable name of the first refused process.
	Process string `json:"process"`
	Count   int    `json:"count"`
	// Recovery names what can open the operation.
	Recovery RefusalRecovery `json:"recovery"`
	// Grant is the value the recovery's capability names; empty when it takes none.
	Grant string `json:"grant,omitempty"`
	// Layer is the filesystem layer that refused a path.
	Layer FloorLayer `json:"-"`
}

// RefusalRecovery identifies the capability that permits a refused operation.
type RefusalRecovery string

const (
	RecoverWriteRoot      RefusalRecovery = "write_root"
	RecoverReadPath       RefusalRecovery = "read_path"
	RecoverSocketPath     RefusalRecovery = "socket_paths"
	RecoverLocalListen    RefusalRecovery = "local_listen"
	RecoverProcessControl RefusalRecovery = "process_control"
	// RecoverOutboundPort covers a connection the kernel names only by port:
	// loopback_connect opens it on this machine, direct_ip elsewhere.
	RecoverOutboundPort RefusalRecovery = "loopback_connect_or_direct_ip"
	// RecoverHostExecution is an operation no sandbox capability admits.
	RecoverHostExecution RefusalRecovery = "host_execution"
	// RecoverNone is a floor no approval opens.
	RecoverNone RefusalRecovery = "none"
)

// RefusalWitness states how completely the kernel's reports cover an action.
type RefusalWitness string

const (
	// WitnessKernel means no reporting gap was detected; silent loss remains possible.
	WitnessKernel RefusalWitness = "kernel"
	// WitnessIncomplete marks a stream gap, reported loss, or failed settle probe.
	WitnessIncomplete RefusalWitness = "incomplete"
	// WitnessUnavailable means this host cannot read kernel refusal reports.
	WitnessUnavailable RefusalWitness = "unavailable"
)

// SandboxRefusals is what the kernel reported refusing one action.
type SandboxRefusals struct {
	Witness  RefusalWitness
	Refusals []SandboxRefusal
	// Omitted counts distinct refusals past the retained bound.
	Omitted int
}

// Paths returns the refused filesystem paths for access, with the grant their
// layer's recovery names.
func (s SandboxRefusals) Paths(access FilesystemAccess) []BoundaryPath {
	var out []BoundaryPath
	seen := map[string]bool{}
	for _, r := range s.Refusals {
		if operationAccess(r.Operation) != access || r.Layer == "" || seen[r.Target] {
			continue
		}
		seen[r.Target] = true
		out = append(out, BoundaryPath{Path: r.Target, Access: access, Layer: r.Layer, Grant: r.Grant})
	}
	return out
}

// With returns the refusals whose recovery is one of recoveries.
func (s SandboxRefusals) With(recoveries ...RefusalRecovery) []SandboxRefusal {
	var out []SandboxRefusal
	for _, r := range s.Refusals {
		for _, want := range recoveries {
			if r.Recovery == want {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Display is the refusal as copy quotes it: operation, target, and process.
func (r SandboxRefusal) Display() string {
	text := r.Operation
	if r.Target != "" {
		text += " " + r.Target
	}
	if r.Process != "" {
		text += " (" + r.Process + ")"
	}
	return text
}

// Port is the port a network refusal names; empty for any other target.
// The kernel names network peers as `remote:<address>:<port>` and listeners
// as `local:<address>:<port>`.
func (r SandboxRefusal) Port() string {
	rest, ok := strings.CutPrefix(r.Target, "remote:")
	if !ok {
		rest, ok = strings.CutPrefix(r.Target, "local:")
	}
	if !ok {
		return ""
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return ""
	}
	return rest[i+1:]
}

// BoundaryPath is a filesystem path the applied boundary refused.
type BoundaryPath struct {
	Path   string
	Access FilesystemAccess
	Layer  FloorLayer
	// Grant is the path the layer's recovery asks for; empty when terminal.
	Grant string
}

// routeRefusal derives recovery from applied rules; truncated targets get no path grant.
func routeRefusal(rules FilesystemRules, operation, target string, truncated bool) SandboxRefusal {
	r := routeOperation(rules, operation, target)
	if truncated {
		r.Target += truncatedReportSuffix
		switch r.Recovery {
		case RecoverWriteRoot, RecoverReadPath, RecoverSocketPath:
			r.Recovery, r.Grant, r.Layer = RecoverNone, "", ""
		case RecoverLocalListen, RecoverProcessControl, RecoverOutboundPort, RecoverHostExecution, RecoverNone:
			// These recoveries do not grant a path from the truncated target.
		}
	}
	return r
}

func routeOperation(rules FilesystemRules, operation, target string) SandboxRefusal {
	r := SandboxRefusal{Operation: operation, Target: target}
	if access := operationAccess(operation); access != "" {
		return routeFilesystemRefusal(rules, r, access)
	}
	switch {
	case operation == "network-outbound" && strings.HasPrefix(target, "/"):
		r.Recovery, r.Grant = RecoverSocketPath, target
	case operation == "network-outbound":
		r.Recovery = RecoverOutboundPort
	case operation == "network-bind" && strings.HasPrefix(target, "/"):
		// No capability grants a unix-socket listener inside the sandbox.
		r.Recovery = RecoverHostExecution
	case operation == "network-bind", operation == "network-inbound":
		r.Recovery = RecoverLocalListen
	case operation == "signal":
		r.Recovery = RecoverProcessControl
	default:
		r.Recovery = RecoverHostExecution
	}
	return r
}

func routeFilesystemRefusal(rules FilesystemRules, r SandboxRefusal, access FilesystemAccess) SandboxRefusal {
	r.Recovery = RecoverNone
	verdict := rules.Verdict(access, r.Target)
	if verdict.Allowed {
		// The rendered rules admit the path, so no grant would change the answer.
		return r
	}
	r.Layer = verdict.Layer
	recovery := verdict.Layer.Recovery()
	if recovery.Terminal() {
		return r
	}
	r.Recovery = RefusalRecovery(recovery.Capability)
	r.Grant = recovery.GrantPath(r.Target, rules.roots...)
	return r
}

// operationAccess classifies the kernel's filesystem operations; empty for
// any other operation.
func operationAccess(operation string) FilesystemAccess {
	switch {
	case strings.HasPrefix(operation, "file-read"):
		return AccessRead
	case strings.HasPrefix(operation, "file-write"), operation == "file-link":
		return AccessWrite
	default:
		return ""
	}
}
