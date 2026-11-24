package command

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/sshproxy"
	"github.com/lycaon/lycaon/internal/tools"
)

const maxSSHDiagnosticCapture = 8192

type sshDiagnosticCapture struct {
	buf []byte
	max int
}

func (b *sshDiagnosticCapture) Write(p []byte) (int, error) {
	total := len(p)
	if b.max <= 0 {
		return total, nil
	}
	remain := b.max - len(b.buf)
	if remain > 0 {
		if len(p) > remain {
			p = p[:remain]
		}
		b.buf = append(b.buf, p...)
	}
	return total, nil
}

func isDirectSSHArgv(argv []string) bool {
	return len(argv) > 0 && filepath.Base(argv[0]) == "ssh"
}

func evaluateDirectSSHStage(
	ctx context.Context,
	tctx tools.ToolContext,
	boundary confine.Boundary,
	commandLine string,
	argv []string,
	stageEnv []string,
	exitCode int,
	stderr []byte,
	network []confine.EgressHost,
	refusals confine.SandboxRefusals,
) (confine.SandboxRefusals, []confine.SSHRecoveryObservation) {
	if !isDirectSSHArgv(argv) || exitCode == 0 || ctx.Err() != nil {
		return refusals, nil
	}
	homeDir := lookupEnvValue(stageEnv, "HOME")
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	inv := sshproxy.ParseDirectSSHInvocation(argv, stageEnv, homeDir)

	// Pre-authentication transport refusals take precedence over SSH trust or signing recovery.
	brokerDenied := slices.ContainsFunc(network, func(h confine.EgressHost) bool {
		return !h.Allowed && !h.Inherited && strings.TrimSpace(h.Host) != ""
	})
	portDenied := slices.ContainsFunc(refusals.Refusals, func(r confine.SandboxRefusal) bool {
		return r.Recovery == confine.RecoverOutboundPort
	})
	if brokerDenied || portDenied {
		filtered, _ := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		return filtered, nil
	}

	if exitCode != 255 {
		filtered, _ := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		return filtered, nil
	}

	agentBound, boundSocketPath := stageHasGrantedSSHSocket(inv.IdentityAgent, tctx.Socket.SocketGrants)
	readableExplicitKey := hasReadableExplicitIdentity(inv.ExplicitIdentityFiles, boundary)
	hostAgent := gitexec.HostSSHAgentSocket(ctx)
	refusedSocket := firstRefusedSSHSocket(refusals, inv.IdentityAgent, hostAgent)

	// 1. Check witnessed or boundary-blocked known_hosts trust read.
	blockedTrust := selectBlockedKnownHostsGrant(refusals, boundary, inv)
	if blockedTrust != "" && inv.StrictHostKeyChecking != "no" && inv.StrictHostKeyChecking != "off" {
		keepSocket := ""
		if !agentBound && !readableExplicitKey {
			if refusedSocket != "" {
				keepSocket = refusedSocket
			} else if hostAgent != "" {
				keepSocket = hostAgent
			}
		}
		filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, blockedTrust, keepSocket, inv.DefaultProbePaths)
		obs := confine.SSHRecoveryObservation{
			Stage:       commandLine,
			Client:      "ssh",
			Destination: inv.Destination,
			Cause:       confine.SSHFailureHostKeyUnreadable,
			TrustPath:   blockedTrust,
			ProbedPaths: probed,
		}
		if keepSocket != "" {
			ref, _ := confine.SocketEndpointReferences(keepSocket)
			obs.AgentSocket = keepSocket
			obs.AgentSocketRef = ref
		}
		return filtered, []confine.SSHRecoveryObservation{obs}
	}

	// 2. Host-key witness against readable known_hosts files in the applied boundary.
	if filtered, obs, matched := evaluateDirectSSHHostKeys(ctx, commandLine, inv, boundary, refusals); matched {
		return filtered, obs
	}

	// 3. Signing / userauth stage after host-key verification succeeded.
	if refusedSocket != "" && !agentBound {
		filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", refusedSocket, inv.DefaultProbePaths)
		ref, _ := confine.SocketEndpointReferences(refusedSocket)
		return filtered, []confine.SSHRecoveryObservation{{
			Stage:          commandLine,
			Client:         "ssh",
			Destination:    inv.Destination,
			Cause:          confine.SSHFailureAgentRequired,
			AgentSocket:    refusedSocket,
			AgentSocketRef: ref,
			ProbedPaths:    probed,
		}}
	}

	if !hasSSHPublicKeyDenial(stderr) {
		filtered, _ := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		return filtered, nil
	}

	if len(inv.ExplicitIdentityFiles) > 0 && !readableExplicitKey {
		filtered, _ := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		return filtered, nil
	}

	if agentBound {
		filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		return filtered, []confine.SSHRecoveryObservation{{
			Stage:       commandLine,
			Client:      "ssh",
			Destination: inv.Destination,
			Cause:       confine.SSHFailurePublicKeyRejected,
			AgentSocket: boundSocketPath,
			ProbedPaths: probed,
		}}
	}

	if hostAgent != "" {
		filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", hostAgent, inv.DefaultProbePaths)
		ref, _ := confine.SocketEndpointReferences(hostAgent)
		return filtered, []confine.SSHRecoveryObservation{{
			Stage:          commandLine,
			Client:         "ssh",
			Destination:    inv.Destination,
			Cause:          confine.SSHFailureAgentRequired,
			AgentSocket:    hostAgent,
			AgentSocketRef: ref,
			ProbedPaths:    probed,
		}}
	}

	filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
	return filtered, []confine.SSHRecoveryObservation{{
		Stage:       commandLine,
		Client:      "ssh",
		Destination: inv.Destination,
		Cause:       confine.SSHFailureAgentUnavailable,
		ProbedPaths: probed,
	}}
}

func evaluateDirectSSHHostKeys(
	ctx context.Context,
	commandLine string,
	inv sshproxy.DirectSSHInvocation,
	boundary confine.Boundary,
	refusals confine.SandboxRefusals,
) (confine.SandboxRefusals, []confine.SSHRecoveryObservation, bool) {
	if inv.StrictHostKeyChecking == "no" || inv.StrictHostKeyChecking == "off" {
		return confine.SandboxRefusals{}, nil, false
	}
	readableKnown := readableKnownHostsFiles(inv, boundary)
	entries := sshproxy.LookupKnownHostKeys(readableKnown, inv.Host, inv.Port)
	if len(entries) == 0 && inv.StrictHostKeyChecking != "accept-new" && !inv.HasProxyCommand && inv.Host != "" {
		filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
		trustPath := ""
		if len(inv.UserKnownHostsFiles) > 0 {
			trustPath = inv.UserKnownHostsFiles[0]
		}
		return filtered, []confine.SSHRecoveryObservation{{
			Stage:       commandLine,
			Client:      "ssh",
			Destination: inv.Destination,
			Cause:       confine.SSHFailureHostKeyUnverified,
			TrustPath:   trustPath,
			ProbedPaths: probed,
		}}, true
	}
	if len(entries) > 0 && !inv.HasProxyCommand && inv.Destination != "" {
		if keyType, keyBlob, ok := sshproxy.ProbeSSHServerHostKey(ctx, inv.Destination); ok {
			if _, conflicted := sshproxy.MatchesKnownHostKey(entries, keyType, keyBlob); conflicted {
				filtered, probed := filterSSHStageRefusals(refusals, boundary, inv.ExplicitIdentityFiles, inv.ExplicitConfigFile, "", "", inv.DefaultProbePaths)
				return filtered, []confine.SSHRecoveryObservation{{
					Stage:       commandLine,
					Client:      "ssh",
					Destination: inv.Destination,
					Cause:       confine.SSHFailureHostKeyChanged,
					TrustPath:   entries[0].SourcePath,
					ProbedPaths: probed,
				}}, true
			}
		}
	}
	return confine.SandboxRefusals{}, nil, false
}

func filterSSHStageRefusals(
	refusals confine.SandboxRefusals,
	boundary confine.Boundary,
	explicitIdentityFiles []string,
	explicitConfigFile, keepTrustPath, keepSocketPath string,
	defaultProbePaths []string,
) (confine.SandboxRefusals, []string) {
	keepTrust := canonicalPathOrClean(keepTrustPath)
	keepSocket := canonicalPathOrClean(keepSocketPath)
	explicitCfg := canonicalPathOrClean(explicitConfigFile)

	var kept []confine.SandboxRefusal
	var probed []string
	hasKeptTrust := false
	hasKeptSocket := false

	for _, r := range refusals.Refusals {
		switch r.Recovery {
		case confine.RecoverReadPath:
			cand := canonicalPathOrClean(firstNonEmptyStr(r.Grant, r.Target))
			if keepTrust != "" && cand == keepTrust {
				kept = append(kept, r)
				hasKeptTrust = true
				continue
			}
			if cand != "" && (cand == explicitCfg || slices.Contains(explicitIdentityFiles, cand)) {
				kept = append(kept, r)
				continue
			}
			if sshproxy.IsSSHClientReadProbe(cand) {
				if cand != "" && !slices.Contains(probed, cand) {
					probed = append(probed, cand)
				}
				continue
			}
			kept = append(kept, r)
		case confine.RecoverSocketPath:
			cand := canonicalPathOrClean(firstNonEmptyStr(r.Grant, r.Target))
			if keepSocket != "" && cand == keepSocket {
				kept = append(kept, r)
				hasKeptSocket = true
				continue
			}
			if cand != "" && !isSystemServiceSocket(cand) && !slices.Contains(probed, cand) {
				probed = append(probed, cand)
			}
		default:
			kept = append(kept, r)
		}
	}

	// Record default ~/.ssh probes that exist on disk and were denied by the boundary even on
	// platforms (such as Landlock) where file-read denials are not kernel-logged individually.
	for _, p := range defaultProbePaths {
		cand := canonicalPathOrClean(p)
		if cand == "" || cand == keepTrust || cand == explicitCfg || slices.Contains(explicitIdentityFiles, cand) || slices.Contains(probed, cand) {
			continue
		}
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			if boundary.Applied && !boundary.Filesystem.Verdict(confine.AccessRead, cand).Allowed {
				probed = append(probed, cand)
			}
		}
	}

	if keepTrust != "" && !hasKeptTrust {
		layer := confine.FloorReadKeyMaterial
		if boundary.Applied {
			if v := boundary.Filesystem.Verdict(confine.AccessRead, keepTrust); !v.Allowed && v.Layer != "" {
				layer = v.Layer
			}
		}
		kept = append(kept, confine.SandboxRefusal{
			Operation: "file-read-data",
			Target:    keepTrust,
			Process:   "ssh",
			Count:     1,
			Layer:     layer,
			Recovery:  confine.RecoverReadPath,
			Grant:     keepTrust,
		})
	}
	if keepSocket != "" && !hasKeptSocket {
		kept = append(kept, confine.SandboxRefusal{
			Operation: "network-outbound",
			Target:    keepSocketPath,
			Process:   "ssh",
			Count:     1,
			Recovery:  confine.RecoverSocketPath,
			Grant:     keepSocketPath,
		})
	}
	return confine.SandboxRefusals{Witness: refusals.Witness, Refusals: kept, Omitted: refusals.Omitted}, probed
}

func selectBlockedKnownHostsGrant(refusals confine.SandboxRefusals, boundary confine.Boundary, inv sshproxy.DirectSSHInvocation) string {
	readable := readableKnownHostsFiles(inv, boundary)
	if inv.Host != "" && len(sshproxy.LookupKnownHostKeys(readable, inv.Host, inv.Port)) > 0 {
		return ""
	}
	var blocked []string
	allKnown := append(slices.Clone(inv.UserKnownHostsFiles), inv.GlobalKnownHostsFiles...)
	for _, r := range refusals.Refusals {
		if r.Recovery != confine.RecoverReadPath {
			continue
		}
		cand := canonicalPathOrClean(firstNonEmptyStr(r.Grant, r.Target))
		if cand != "" && sshproxy.IsSSHTrustPath(cand, allKnown) && !slices.Contains(blocked, cand) {
			blocked = append(blocked, cand)
		}
	}
	if boundary.Applied {
		for _, p := range allKnown {
			cand := canonicalPathOrClean(p)
			if cand == "" || slices.Contains(blocked, cand) {
				continue
			}
			if info, err := os.Stat(cand); err != nil || info.IsDir() {
				continue
			}
			v := boundary.Filesystem.Verdict(confine.AccessRead, cand)
			if !v.Allowed && v.Layer.Recovery().Capability == "read_path" {
				blocked = append(blocked, v.Layer.Recovery().GrantPath(cand))
			}
		}
	}
	for _, p := range blocked {
		if inv.Host != "" && len(sshproxy.LookupKnownHostKeys([]string{p}, inv.Host, inv.Port)) > 0 {
			return p
		}
	}
	for _, p := range blocked {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return p
		}
	}
	if len(blocked) > 0 {
		return blocked[0]
	}
	return ""
}

func readableKnownHostsFiles(inv sshproxy.DirectSSHInvocation, boundary confine.Boundary) []string {
	var out []string
	for _, p := range append(slices.Clone(inv.UserKnownHostsFiles), inv.GlobalKnownHostsFiles...) {
		cand := canonicalPathOrClean(p)
		if cand == "" {
			continue
		}
		if !boundary.Applied || boundary.Filesystem.Verdict(confine.AccessRead, cand).Allowed {
			out = append(out, cand)
		}
	}
	return out
}

func stageHasGrantedSSHSocket(identityAgent string, grants []confine.SocketGrant) (bool, string) {
	raw := strings.TrimSpace(identityAgent)
	if raw == "" || len(grants) == 0 {
		return false, ""
	}
	const prefix = "{{paintedwolf-socket:"
	const suffix = "}}"
	if !strings.HasPrefix(raw, prefix) || !strings.HasSuffix(raw, suffix) {
		return false, ""
	}
	body := strings.TrimPrefix(strings.TrimSuffix(raw, suffix), prefix)
	body = strings.TrimPrefix(body, "unix-url:")
	decoded, err := url.PathUnescape(body)
	if err != nil || decoded == "" {
		return false, ""
	}
	resolved := fspath.CanonicalPath(decoded)
	for _, g := range grants {
		if g.ApprovedPath == decoded || (resolved != "" && g.ResolvedPath == resolved) {
			return true, g.ApprovedPath
		}
	}
	return false, ""
}

func hasReadableExplicitIdentity(explicit []string, boundary confine.Boundary) bool {
	for _, idPath := range explicit {
		if info, err := os.Stat(idPath); err != nil || info.IsDir() {
			continue
		}
		if !boundary.Applied || boundary.Filesystem.Verdict(confine.AccessRead, idPath).Allowed {
			return true
		}
	}
	return false
}

func firstRefusedSSHSocket(refusals confine.SandboxRefusals, identityAgent, hostAgent string) string {
	var preferred []string
	for _, raw := range []string{identityAgent, hostAgent} {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "none" || raw == "SSH_AUTH_SOCK" || strings.HasPrefix(raw, "{{paintedwolf-socket:") {
			continue
		}
		preferred = append(preferred, canonicalPathOrClean(raw))
	}
	for _, r := range refusals.Refusals {
		if r.Recovery != confine.RecoverSocketPath {
			continue
		}
		g := strings.TrimSpace(r.Grant)
		if g == "" {
			continue
		}
		if slices.Contains(preferred, canonicalPathOrClean(g)) {
			return g
		}
	}
	for _, r := range refusals.Refusals {
		if r.Recovery != confine.RecoverSocketPath {
			continue
		}
		g := strings.TrimSpace(r.Grant)
		if g != "" && !isSystemServiceSocket(canonicalPathOrClean(g)) {
			return g
		}
	}
	return ""
}

func isSystemServiceSocket(path string) bool {
	return path == "/run/nscd/socket" ||
		path == "/var/run/nscd/socket" ||
		path == "/var/run/mDNSResponder" ||
		strings.HasPrefix(path, "/run/systemd/") ||
		strings.HasPrefix(path, "/var/run/syslog")
}

func hasSSHPublicKeyDenial(stderr []byte) bool {
	if len(stderr) == 0 {
		return false
	}
	lower := bytes.ToLower(stderr)
	return bytes.Contains(lower, []byte("permission denied (")) && bytes.Contains(lower, []byte("publickey"))
}

func lookupEnvValue(entries []string, key string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(entries[i], "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}

func canonicalPathOrClean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if c := fspath.CanonicalPath(p); c != "" {
		return c
	}
	return filepath.Clean(p)
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
