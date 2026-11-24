package gitcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/sshproxy"
)

type gitSSHLaunchInput struct {
	commandLine string
	dest        string
	host        string
	port        uint16
	mediated    bool
	confinement *confine.Confinement
	lease       *confine.ActionLease
	hasAgent    bool
	agentPath   string
	exitCode    int
	failed      bool
	wroteStdout bool
	diagnostics []byte
	transport   confine.GitTransportObservation
}

func evaluateGitSSHRecovery(ctx context.Context, in gitSSHLaunchInput) (*confine.SSHRecoveryObservation, []confine.SandboxRefusal, bool) {
	if !in.failed {
		return nil, nil, false
	}
	if in.mediated {
		return evaluateMediatedGitSSH(ctx, in)
	}
	return evaluateDirectGitSSH(ctx, in)
}

func evaluateMediatedGitSSH(ctx context.Context, in gitSSHLaunchInput) (*confine.SSHRecoveryObservation, []confine.SandboxRefusal, bool) {
	if in.transport.Cause.PreAuthFailure() {
		return nil, nil, true
	}
	if in.transport.Cause != confine.GitTransportCauseEstablished || len(in.transport.Attempts) == 0 {
		return nil, nil, true
	}
	last := in.transport.Attempts[len(in.transport.Attempts)-1]
	dest := firstString(last.Destination, in.dest)

	if last.SawServerBanner && !last.ReachedUserAuth {
		var files []string
		if in.confinement != nil {
			files = confine.MediatedKnownHostsFiles(*in.confinement)
		}
		entries := sshproxy.LookupKnownHostKeys(files, last.Host, last.Port)
		if len(entries) > 0 {
			return &confine.SSHRecoveryObservation{
				Stage:       in.commandLine,
				Client:      "git",
				Destination: dest,
				Cause:       confine.SSHFailureHostKeyChanged,
				TrustPath:   entries[0].SourcePath,
			}, nil, true
		}
		return &confine.SSHRecoveryObservation{
			Stage:       in.commandLine,
			Client:      "git",
			Destination: dest,
			Cause:       confine.SSHFailureHostKeyUnverified,
		}, nil, true
	}

	if !last.ReachedUserAuth {
		return nil, nil, true
	}

	if refusedSock := leaseRefusedSSHSocket(ctx, in.lease); refusedSock != "" && !in.hasAgent {
		ref, _ := confine.SocketEndpointReferences(refusedSock)
		return &confine.SSHRecoveryObservation{
			Stage:          in.commandLine,
			Client:         "git",
			Destination:    dest,
			Cause:          confine.SSHFailureAgentRequired,
			AgentSocket:    refusedSock,
			AgentSocketRef: ref,
		}, []confine.SandboxRefusal{sshSocketRefusal(refusedSock)}, true
	}

	if in.exitCode != 128 || in.wroteStdout || !hasOpenSSHUserAuthDenial(in.diagnostics) {
		return nil, nil, true
	}

	if in.hasAgent {
		return &confine.SSHRecoveryObservation{
			Stage:       in.commandLine,
			Client:      "git",
			Destination: dest,
			Cause:       confine.SSHFailurePublicKeyRejected,
			AgentSocket: in.agentPath,
		}, nil, true
	}

	hostAgent := gitexec.HostSSHAgentSocket(ctx)
	if hostAgent != "" {
		ref, _ := confine.SocketEndpointReferences(hostAgent)
		return &confine.SSHRecoveryObservation{
			Stage:          in.commandLine,
			Client:         "git",
			Destination:    dest,
			Cause:          confine.SSHFailureAgentRequired,
			AgentSocket:    hostAgent,
			AgentSocketRef: ref,
		}, []confine.SandboxRefusal{sshSocketRefusal(hostAgent)}, true
	}
	return &confine.SSHRecoveryObservation{
		Stage:       in.commandLine,
		Client:      "git",
		Destination: dest,
		Cause:       confine.SSHFailureAgentUnavailable,
	}, nil, true
}

func evaluateDirectGitSSH(ctx context.Context, in gitSSHLaunchInput) (*confine.SSHRecoveryObservation, []confine.SandboxRefusal, bool) {
	if in.exitCode != 128 || in.wroteStdout || in.lease == nil {
		return nil, nil, true
	}
	refusals := in.lease.Refusals()
	if slices.ContainsFunc(refusals.Refusals, func(r confine.SandboxRefusal) bool {
		return r.Recovery == confine.RecoverOutboundPort
	}) {
		return nil, nil, true
	}
	var blockedTrust string
	for _, r := range refusals.Refusals {
		if r.Recovery == confine.RecoverReadPath && sshproxy.IsSSHTrustPath(firstString(r.Grant, r.Target), nil) {
			blockedTrust = firstString(r.Grant, r.Target)
			break
		}
	}
	home, _ := os.UserHomeDir()
	if blockedTrust == "" && in.confinement != nil && home != "" {
		b := confine.BoundaryOf(in.confinement)
		for _, cand := range []string{filepath.Join(home, ".ssh", "known_hosts"), filepath.Join(home, ".ssh", "known_hosts2")} {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				v := b.Filesystem.Verdict(confine.AccessRead, cand)
				if !v.Allowed && v.Layer.Recovery().Capability == "read_path" {
					blockedTrust = v.Layer.Recovery().GrantPath(cand)
					break
				}
			}
		}
	}

	hostAgent := gitexec.HostSSHAgentSocket(ctx)
	if blockedTrust != "" {
		obs := &confine.SSHRecoveryObservation{
			Stage:       in.commandLine,
			Client:      "git",
			Destination: in.dest,
			Cause:       confine.SSHFailureHostKeyUnreadable,
			TrustPath:   blockedTrust,
		}
		synth := []confine.SandboxRefusal{{
			Operation: "file-read-data", Target: blockedTrust, Process: "ssh", Count: 1,
			Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath, Grant: blockedTrust,
		}}
		if !in.hasAgent && hostAgent != "" {
			ref, _ := confine.SocketEndpointReferences(hostAgent)
			obs.AgentSocket = hostAgent
			obs.AgentSocketRef = ref
			synth = append(synth, sshSocketRefusal(hostAgent))
		}
		return obs, synth, true
	}

	var knownFiles []string
	if home != "" {
		knownFiles = append(knownFiles, filepath.Join(home, ".ssh", "known_hosts"), filepath.Join(home, ".ssh", "known_hosts2"))
	}
	knownFiles = append(knownFiles, "/etc/ssh/ssh_known_hosts", "/etc/ssh/ssh_known_hosts2")
	entries := sshproxy.LookupKnownHostKeys(knownFiles, in.host, in.port)
	if len(entries) > 0 && in.dest != "" {
		if keyType, keyBlob, ok := sshproxy.ProbeSSHServerHostKey(ctx, in.dest); ok {
			if _, conflicted := sshproxy.MatchesKnownHostKey(entries, keyType, keyBlob); conflicted {
				return &confine.SSHRecoveryObservation{
					Stage:       in.commandLine,
					Client:      "git",
					Destination: in.dest,
					Cause:       confine.SSHFailureHostKeyChanged,
					TrustPath:   entries[0].SourcePath,
				}, nil, true
			}
		}
	}
	if !hasOpenSSHUserAuthDenial(in.diagnostics) {
		return nil, nil, true
	}
	if in.hasAgent {
		return &confine.SSHRecoveryObservation{
			Stage:       in.commandLine,
			Client:      "git",
			Destination: in.dest,
			Cause:       confine.SSHFailurePublicKeyRejected,
			AgentSocket: in.agentPath,
		}, nil, true
	}
	if hostAgent != "" {
		ref, _ := confine.SocketEndpointReferences(hostAgent)
		return &confine.SSHRecoveryObservation{
			Stage:          in.commandLine,
			Client:         "git",
			Destination:    in.dest,
			Cause:          confine.SSHFailureAgentRequired,
			AgentSocket:    hostAgent,
			AgentSocketRef: ref,
		}, []confine.SandboxRefusal{sshSocketRefusal(hostAgent)}, true
	}
	return &confine.SSHRecoveryObservation{
		Stage:       in.commandLine,
		Client:      "git",
		Destination: in.dest,
		Cause:       confine.SSHFailureAgentUnavailable,
	}, nil, true
}

func leaseRefusedSSHSocket(ctx context.Context, lease *confine.ActionLease) string {
	if lease == nil {
		return ""
	}
	hostAgent := strings.TrimSpace(gitexec.HostSSHAgentSocket(ctx))
	refusals := lease.Refusals().Refusals
	if hostAgent != "" {
		for _, r := range refusals {
			if r.Recovery == confine.RecoverSocketPath && filepath.Clean(strings.TrimSpace(r.Grant)) == filepath.Clean(hostAgent) {
				return strings.TrimSpace(r.Grant)
			}
		}
	}
	for _, r := range refusals {
		g := strings.TrimSpace(r.Grant)
		if r.Recovery == confine.RecoverSocketPath && g != "" && !isSystemResolverSocket(filepath.Clean(g)) {
			return g
		}
	}
	return ""
}

func isSystemResolverSocket(path string) bool {
	return path == "/run/nscd/socket" ||
		path == "/var/run/nscd/socket" ||
		path == "/var/run/mDNSResponder" ||
		strings.HasPrefix(path, "/run/systemd/") ||
		strings.HasPrefix(path, "/var/run/syslog")
}

func sshSocketRefusal(socketPath string) confine.SandboxRefusal {
	return confine.SandboxRefusal{
		Operation: "network-outbound",
		Target:    socketPath,
		Process:   "ssh",
		Count:     1,
		Recovery:  confine.RecoverSocketPath,
		Grant:     socketPath,
	}
}

func hasOpenSSHUserAuthDenial(diagnostics []byte) bool {
	if len(diagnostics) == 0 {
		return false
	}
	lower := bytes.ToLower(diagnostics)
	return bytes.Contains(lower, []byte("permission denied (")) && bytes.Contains(lower, []byte("publickey"))
}

func firstString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
