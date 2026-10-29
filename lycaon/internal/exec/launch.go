package exec

import (
	"context"
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
	LaunchDocumentCore    LaunchKind = "document_core"
	LaunchOCREngine       LaunchKind = "ocr_engine"
)

// LaunchPlan carries mandatory process authority and attribution.
type LaunchPlan struct {
	Kind        LaunchKind
	Subject     string
	Confinement *confine.Confinement
	Environment EnvironmentMode
	ExtraEnv    []string
	// TrustedEnvironment is the program's host-built starting environment.
	// AgentLaunchOptions uses it to distinguish host plumbing from exports.
	TrustedEnvironment []string
	PathAuthority      confine.LaunchPaths
	InputLimits        confine.LaunchVectorLimits
	CurrentAuthority   func(context.Context) error
}

func (p LaunchPlan) WithTrustedEnvironment(entries []string) LaunchPlan {
	p.TrustedEnvironment = append([]string(nil), entries...)
	return p
}

// EnvironmentMode selects the process environment.
type EnvironmentMode string

const (
	EnvironmentInherited EnvironmentMode = ""
	EnvironmentReduced   EnvironmentMode = "reduced"
)

// reduced reports whether the launch starts from process plumbing rather than
// the engine's environment. An agent command always does: the engine inherits
// whatever launched it, including another program's session credentials, and
// none of that is the agent's to read.
func (p LaunchPlan) reduced() bool {
	return p.Environment == EnvironmentReduced || p.Kind == LaunchAgentCommand
}

// WithPathAuthority binds the current invocation's existing path authority.
func (p LaunchPlan) WithPathAuthority(paths confine.LaunchPaths) LaunchPlan {
	p.PathAuthority = paths
	return p
}

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

// DocumentCoreLaunch starts the confined text CRDT process.
func DocumentCoreLaunch(confinement *confine.Confinement) LaunchPlan {
	return LaunchPlan{Kind: LaunchDocumentCore, Subject: "document_core", Confinement: confinement}
}

// OCREngineLaunch starts the confined bundled text recognizer for one image.
func OCREngineLaunch(confinement *confine.Confinement) LaunchPlan {
	return LaunchPlan{Kind: LaunchOCREngine, Subject: "ocr_engine", Confinement: confinement}
}

// ScratchDirEnv names the session scratch folder in an agent process's environment.
const ScratchDirEnv = "SCRATCH_DIR"

// AgentLaunch starts a confined agent process.
func AgentLaunch(kind LaunchKind, subject string, confinement *confine.Confinement) LaunchPlan {
	plan := LaunchPlan{Kind: kind, Subject: strings.TrimSpace(subject), Confinement: confinement}
	if kind == LaunchAgentCommand {
		plan.Environment = EnvironmentReduced
	}
	if confinement != nil && strings.TrimSpace(confinement.SessionScratchRoot) != "" {
		scratch := confinement.SessionScratchRoot
		plan.ExtraEnv = []string{ScratchDirEnv + "=" + scratch, "TMPDIR=" + scratch, "TMP=" + scratch, "TEMP=" + scratch}
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
	case LaunchAgentCommand, LaunchBundledScanner, LaunchManagedBrowser, LaunchLocalMCP, LaunchDocumentCore, LaunchOCREngine:
		if err := confine.RequireApplied(p.Confinement != nil); err != nil {
			return err
		}
		if p.Confinement != nil && p.Confinement.Network == confine.NetworkProxyOnly && !confine.EgressBound(p.Confinement) {
			return fmt.Errorf("proxy confinement requires a live action lease")
		}
		return nil
	default:
		return fmt.Errorf("unknown process launch kind %q", p.Kind)
	}
}

// AgentLaunchOptions are the directory and environment inputs of one confined
// agent launch. Launches and launch lookups (which, type, command -v) build
// their options here, so discovery selects exactly what a launch would run.
// A program's exported NAME=value entries apply over the reduced environment
// and the boundary's own entries; a key the host reserves refuses with
// ErrBlockedEnvKey rather than being dropped.
func AgentLaunchOptions(launch LaunchPlan, dir string, environment, pathExtra []string) (ExecOpts, error) {
	launch = launch.WithReducedEnvironment()
	opts := ExecOpts{Launch: launch, Dir: dir, PathExtra: append([]string(nil), pathExtra...), AppendEnv: []string{"GIT_PAGER=cat"}}
	if len(environment) > 0 {
		defaultPath := defaultAgentPath(pathExtra)
		trusted := make(map[string]string, len(launch.TrustedEnvironment))
		for _, entry := range launch.TrustedEnvironment {
			name, value, ok := strings.Cut(entry, "=")
			if ok {
				trusted[name] = value
			}
		}
		opts.InlineEnv = make(map[string]string, len(environment))
		for _, entry := range environment {
			name, value, _ := strings.Cut(entry, "=")
			trimmed := strings.TrimSpace(name)
			if original, ok := trusted[name]; ok && original == value &&
				(hasBlockedPrefix(strings.ToUpper(name)) || strings.EqualFold(name, "GIT_PAGER")) {
				continue
			}
			if defaultPath != "" && strings.EqualFold(trimmed, "PATH") && value == defaultPath {
				continue
			}
			if strings.EqualFold(trimmed, "GIT_PAGER") && value == "cat" {
				continue
			}
			opts.InlineEnv[name] = value
		}
		if err := ValidateInlineEnv(opts.InlineEnv); err != nil {
			return ExecOpts{Launch: launch}, err
		}
	}
	return opts, nil
}

func defaultAgentPath(pathExtra []string) string {
	env, err := buildProcessEnv(ReducedEnviron(), nil, pathExtra)
	if err != nil {
		return ""
	}
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == "PATH" {
			return v
		}
	}
	return ""
}
