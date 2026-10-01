package exec

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// LaunchKind identifies process purpose.
type LaunchKind string

const (
	LaunchHostInternal    LaunchKind = "host_internal"
	LaunchAgentCommand    LaunchKind = "agent_command"
	LaunchBundledScanner  LaunchKind = "bundled_scanner"
	LaunchExternalScanner LaunchKind = "external_scanner"
	LaunchManagedBrowser  LaunchKind = "managed_browser"
	LaunchLocalMCP        LaunchKind = "local_mcp"
)

// LaunchPlan carries mandatory process authority and attribution.
type LaunchPlan struct {
	Kind        LaunchKind
	Subject     string
	Confinement *confine.Confinement
	Environment EnvironmentMode
	ExtraEnv    []string
}

// EnvironmentMode selects the process environment.
type EnvironmentMode string

const (
	EnvironmentInherited EnvironmentMode = ""
	EnvironmentReduced   EnvironmentMode = "reduced"
)

// WithReducedEnvironment removes ambient credentials.
func (p LaunchPlan) WithReducedEnvironment() LaunchPlan {
	p.Environment = EnvironmentReduced
	return p
}

// WithExtraEnv appends explicit environment variables to the launch plan.
func (p LaunchPlan) WithExtraEnv(entries ...string) LaunchPlan {
	p.ExtraEnv = append(append([]string(nil), p.ExtraEnv...), entries...)
	return p
}

// HostLaunch identifies a host-managed maintenance process.
func HostLaunch(subject string) LaunchPlan {
	return LaunchPlan{Kind: LaunchHostInternal, Subject: strings.TrimSpace(subject)}
}

// ExternalScannerLaunch starts a user-installed scanner CLI.
func ExternalScannerLaunch(subject string) LaunchPlan {
	return LaunchPlan{Kind: LaunchExternalScanner, Subject: strings.TrimSpace(subject)}
}

// BundledScannerLaunch starts a confined bundled scanner subprocess.
func BundledScannerLaunch(subject string, confinement *confine.Confinement) LaunchPlan {
	return LaunchPlan{Kind: LaunchBundledScanner, Subject: strings.TrimSpace(subject), Confinement: confinement}
}

// ScratchDirEnv names the session scratch folder in an agent process's environment.
const ScratchDirEnv = "SCRATCH_DIR"

// AgentLaunch starts a confined agent process.
func AgentLaunch(kind LaunchKind, subject string, confinement *confine.Confinement) LaunchPlan {
	plan := LaunchPlan{Kind: kind, Subject: strings.TrimSpace(subject), Confinement: confinement}
	if confinement != nil && strings.TrimSpace(confinement.SessionScratchRoot) != "" {
		plan.ExtraEnv = []string{ScratchDirEnv + "=" + strings.TrimSpace(confinement.SessionScratchRoot)}
	}
	return plan
}

func (p LaunchPlan) validate() error {
	if p.Kind == "" {
		return fmt.Errorf("process launch plan is required")
	}
	if strings.TrimSpace(p.Subject) == "" {
		return fmt.Errorf("process launch subject is required")
	}
	if p.Environment != EnvironmentInherited && p.Environment != EnvironmentReduced {
		return fmt.Errorf("unknown process environment mode %q", p.Environment)
	}
	if p.Confinement != nil && p.Confinement.HostExecution && p.Kind != LaunchAgentCommand {
		return fmt.Errorf("host execution requires an agent command launch")
	}
	switch p.Kind {
	case LaunchHostInternal:
		if p.Confinement != nil {
			return fmt.Errorf("host-internal launch cannot carry agent confinement")
		}
		return nil
	case LaunchExternalScanner:
		if p.Confinement != nil {
			return fmt.Errorf("external scanner launch cannot carry confinement")
		}
		return nil
	case LaunchAgentCommand, LaunchBundledScanner, LaunchManagedBrowser, LaunchLocalMCP:
		if p.Confinement == nil && confine.Enforcing() {
			return confine.ErrNotConfined
		}
		if p.Confinement != nil && p.Confinement.Network == confine.NetworkProxyOnly && !confine.EgressBound(p.Confinement) {
			return fmt.Errorf("proxy confinement requires a live action lease")
		}
		return nil
	default:
		return fmt.Errorf("unknown process launch kind %q", p.Kind)
	}
}
