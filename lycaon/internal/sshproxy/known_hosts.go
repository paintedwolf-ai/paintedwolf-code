package sshproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // OpenSSH known_hosts |1| host hashing specifies HMAC-SHA1.
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lycaon/lycaon/internal/fspath"
)

var (
	defaultUserKnownHostsRel  = []string{".ssh/known_hosts", ".ssh/known_hosts2"}
	defaultSystemKnownHosts   = []string{"/etc/ssh/ssh_known_hosts", "/etc/ssh/ssh_known_hosts2"}
	defaultSSHProbeFileNames  = []string{"config", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "id_xmss", "id_ed25519_sk", "id_ecdsa_sk", "identity"}
	errStopAfterServerHostKey = errors.New("ssh host key captured")
)

// KnownHostEntry is one trusted public key entry matched in an OpenSSH known_hosts file.
type KnownHostEntry struct {
	SourcePath string
	KeyType    string
	KeyBlob    []byte
}

// LookupKnownHostKeys returns every non-revoked host key entry matching (host, port) across paths.
func LookupKnownHostKeys(paths []string, host string, port uint16) []KnownHostEntry {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	candidates := knownHostLookupTokens(host, port)
	var entries []KnownHostEntry
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		f, err := os.Open(p) // #nosec G304 -- host inspects known_hosts paths declared or configured for SSH verification
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 4096), 256*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			marker := ""
			if strings.HasPrefix(line, "@") {
				var rest string
				marker, rest, _ = cutWhitespace(line)
				line = strings.TrimSpace(rest)
			}
			if marker == "@revoked" || marker == "@cert-authority" {
				continue
			}
			hostsField, rest, ok := cutWhitespace(line)
			if !ok {
				continue
			}
			keyType, keyRest, ok := cutWhitespace(strings.TrimSpace(rest))
			if !ok {
				continue
			}
			keyB64, _, _ := cutWhitespace(strings.TrimSpace(keyRest))
			if keyB64 == "" || !knownHostsFieldMatches(hostsField, candidates) {
				continue
			}
			rawKey, err := base64.StdEncoding.DecodeString(keyB64)
			if err != nil {
				continue
			}
			pub, err := ssh.ParsePublicKey(rawKey)
			if err != nil {
				continue
			}
			entries = append(entries, KnownHostEntry{
				SourcePath: p,
				KeyType:    keyType,
				KeyBlob:    pub.Marshal(),
			})
		}
		_ = f.Close()
	}
	return entries
}

// MatchesKnownHostKey reports whether presentedBlob matches at least one entry in entries
// and whether it conflicts with existing entries for the host.
func MatchesKnownHostKey(entries []KnownHostEntry, keyType string, presentedBlob []byte) (matched, conflicted bool) {
	if len(entries) == 0 || len(presentedBlob) == 0 {
		return false, false
	}
	for _, e := range entries {
		if bytes.Equal(e.KeyBlob, presentedBlob) {
			return true, false
		}
	}
	_ = keyType
	return false, true
}

// ProbeSSHServerHostKey performs an unauthenticated SSH key-exchange probe to capture the
// server's presented public host key, aborting inside HostKeyCallback before SSH_MSG_NEWKEYS.
func ProbeSSHServerHostKey(ctx context.Context, addr string) (keyType string, keyBlob []byte, ok bool) {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	var d net.Dialer
	rawConn, err := d.DialContext(probeCtx, "tcp", addr)
	if err != nil {
		return "", nil, false
	}
	defer func() { _ = rawConn.Close() }()
	if deadline, hasDeadline := probeCtx.Deadline(); hasDeadline {
		_ = rawConn.SetDeadline(deadline)
	}

	cfg := &ssh.ClientConfig{
		User: "probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			keyType = key.Type()
			keyBlob = append([]byte(nil), key.Marshal()...)
			return errStopAfterServerHostKey
		},
		Timeout: 1500 * time.Millisecond,
	}
	c, _, _, err := ssh.NewClientConn(rawConn, addr, cfg)
	if c != nil {
		_ = c.Close()
	}
	if len(keyBlob) > 0 && errors.Is(err, errStopAfterServerHostKey) {
		return keyType, keyBlob, true
	}
	return "", nil, false
}

// DirectSSHInvocation holds parsed OpenSSH client options and target endpoint from argv and env.
type DirectSSHInvocation struct {
	Host                  string
	Port                  uint16
	Destination           string
	StrictHostKeyChecking string
	UserKnownHostsFiles   []string
	GlobalKnownHostsFiles []string
	ExplicitIdentityFiles []string
	ExplicitConfigFile    string
	IdentityAgent         string
	HasProxyCommand       bool
	DefaultProbePaths     []string
}

const (
	sshShortFlagsNoArg   = "46AaCfGgKkMNnqsTtVvXxYy"
	sshShortFlagsWithArg = "BbcDEeFIiJLlmOopQRSWw"
)

// ParseDirectSSHInvocation extracts target host, port, known_hosts files, explicit identity files
// and agent configuration from an OpenSSH client argv and environment.
func ParseDirectSSHInvocation(argv []string, env []string, homeDir string) DirectSSHInvocation {
	inv := DirectSSHInvocation{
		Port:                  22,
		StrictHostKeyChecking: "ask",
		GlobalKnownHostsFiles: append([]string(nil), defaultSystemKnownHosts...),
	}
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == "SSH_AUTH_SOCK" && strings.TrimSpace(v) != "" {
			inv.IdentityAgent = strings.TrimSpace(v)
		}
	}
	var userKnownHosts []string
	applyOption := func(opt string) {
		k, v, ok := splitSSHOption(opt)
		if !ok {
			return
		}
		switch strings.ToLower(k) {
		case "stricthostkeychecking":
			inv.StrictHostKeyChecking = strings.ToLower(strings.Trim(v, `"'`))
		case "userknownhostsfile":
			for _, token := range splitSSHPathList(v) {
				if strings.EqualFold(token, "none") {
					userKnownHosts = []string{}
					continue
				}
				userKnownHosts = append(userKnownHosts, canonicalOrClean(expandHomePath(token, homeDir)))
			}
		case "globalknownhostsfile":
			inv.GlobalKnownHostsFiles = nil
			for _, token := range splitSSHPathList(v) {
				if !strings.EqualFold(token, "none") {
					inv.GlobalKnownHostsFiles = append(inv.GlobalKnownHostsFiles, canonicalOrClean(expandHomePath(token, homeDir)))
				}
			}
		case "identityfile":
			p := expandHomePath(strings.Trim(v, `"'`), homeDir)
			if p != "" {
				inv.ExplicitIdentityFiles = append(inv.ExplicitIdentityFiles, canonicalOrClean(p))
			}
		case "identityagent":
			val := strings.Trim(v, `"'`)
			if strings.EqualFold(val, "none") {
				inv.IdentityAgent = ""
			} else if val != "" && !strings.EqualFold(val, "SSH_AUTH_SOCK") {
				inv.IdentityAgent = expandHomePath(val, homeDir)
			}
		case "port":
			if p, err := strconv.ParseUint(strings.Trim(v, `"'`), 10, 16); err == nil && p > 0 {
				inv.Port = uint16(p)
			}
		case "proxycommand", "proxyjump":
			if !strings.EqualFold(strings.Trim(v, `"'`), "none") {
				inv.HasProxyCommand = true
			}
		}
	}

	var positionals []string
	endFlags := false
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if endFlags {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			endFlags = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			endFlags = true
			continue
		}
		flags := arg[1:]
		for j := 0; j < len(flags); j++ {
			ch := flags[j]
			if strings.IndexByte(sshShortFlagsNoArg, ch) >= 0 {
				continue
			}
			if strings.IndexByte(sshShortFlagsWithArg, ch) >= 0 {
				var val string
				if j+1 < len(flags) {
					val = flags[j+1:]
				} else if i+1 < len(argv) {
					i++
					val = argv[i]
				}
				switch ch {
				case 'p':
					if p, err := strconv.ParseUint(val, 10, 16); err == nil && p > 0 {
						inv.Port = uint16(p)
					}
				case 'i':
					if p := expandHomePath(val, homeDir); p != "" {
						inv.ExplicitIdentityFiles = append(inv.ExplicitIdentityFiles, canonicalOrClean(p))
					}
				case 'F':
					if !strings.EqualFold(val, "none") && val != "/dev/null" {
						inv.ExplicitConfigFile = canonicalOrClean(expandHomePath(val, homeDir))
					}
				case 'J':
					inv.HasProxyCommand = true
				case 'o':
					applyOption(val)
				}
				break
			}
			break
		}
	}
	if userKnownHosts != nil {
		inv.UserKnownHostsFiles = userKnownHosts
	} else if homeDir != "" {
		for _, rel := range defaultUserKnownHostsRel {
			inv.UserKnownHostsFiles = append(inv.UserKnownHostsFiles, canonicalOrClean(filepath.Join(homeDir, rel)))
		}
	}
	if homeDir != "" {
		for _, name := range defaultSSHProbeFileNames {
			inv.DefaultProbePaths = append(inv.DefaultProbePaths, canonicalOrClean(filepath.Join(homeDir, ".ssh", name)))
		}
	}
	if len(positionals) > 0 {
		target := positionals[0]
		if strings.HasPrefix(strings.ToLower(target), "ssh://") {
			if host, port := parseSSHURLTarget(target); host != "" {
				inv.Host = host
				if port > 0 {
					inv.Port = port
				}
			}
		} else {
			if at := strings.LastIndexByte(target, '@'); at >= 0 {
				target = target[at+1:]
			}
			inv.Host = strings.ToLower(strings.Trim(target, "[]"))
		}
	}
	if inv.Host != "" {
		inv.Destination = net.JoinHostPort(inv.Host, strconv.Itoa(int(inv.Port)))
	}
	return inv
}

// IsSSHTrustPath reports whether path names an SSH known_hosts trust file.
func IsSSHTrustPath(path string, explicitKnownHosts []string) bool {
	cand := canonicalOrClean(path)
	if cand == "" {
		return false
	}
	for _, exp := range explicitKnownHosts {
		if canonicalOrClean(exp) == cand {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(cand))
	return strings.HasPrefix(base, "known_hosts") || strings.HasPrefix(base, "ssh_known_hosts")
}

// IsSSHClientReadProbe reports whether path lies inside ~/.ssh or /etc/ssh.
func IsSSHClientReadProbe(path string) bool {
	cand := canonicalOrClean(path)
	if cand == "" {
		return false
	}
	slashed := filepath.ToSlash(cand)
	return strings.Contains(slashed, "/.ssh/") || strings.HasSuffix(slashed, "/.ssh") || strings.HasPrefix(slashed, "/etc/ssh/")
}

func knownHostLookupTokens(host string, port uint16) []string {
	bracketed := "[" + host + "]:" + strconv.Itoa(int(port))
	if port == 0 || port == 22 {
		return []string{host, "[" + host + "]:22"}
	}
	return []string{bracketed}
}

func knownHostsFieldMatches(field string, candidates []string) bool {
	matched := false
	for pattern := range strings.SplitSeq(field, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = pattern[1:]
		}
		hit := false
		if strings.HasPrefix(pattern, "|1|") {
			for _, cand := range candidates {
				if matchHashedKnownHost(pattern, cand) {
					hit = true
					break
				}
			}
		} else {
			lowerPattern := strings.ToLower(pattern)
			for _, cand := range candidates {
				if ok, _ := filepath.Match(lowerPattern, strings.ToLower(cand)); ok || lowerPattern == strings.ToLower(cand) {
					hit = true
					break
				}
			}
		}
		if hit {
			if negated {
				return false
			}
			matched = true
		}
	}
	return matched
}

func matchHashedKnownHost(pattern, candidate string) bool {
	parts := strings.Split(pattern, "|")
	if len(parts) != 4 || parts[1] != "1" {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	wantMAC, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	_, _ = mac.Write([]byte(candidate))
	return hmac.Equal(mac.Sum(nil), wantMAC)
}

func cutWhitespace(s string) (head, rest string, ok bool) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

func splitSSHOption(opt string) (key, val string, ok bool) {
	opt = strings.TrimSpace(opt)
	if opt == "" {
		return "", "", false
	}
	if eq := strings.IndexByte(opt, '='); eq > 0 {
		return strings.TrimSpace(opt[:eq]), strings.TrimSpace(opt[eq+1:]), true
	}
	return cutWhitespace(opt)
}

func splitSSHPathList(raw string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if (ch == ' ' || ch == '\t') && !inQuote {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func expandHomePath(p, homeDir string) string {
	p = strings.TrimSpace(p)
	if p == "~" && homeDir != "" {
		return homeDir
	}
	if strings.HasPrefix(p, "~/") && homeDir != "" {
		return filepath.Join(homeDir, p[2:])
	}
	return p
}

func canonicalOrClean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if c := fspath.CanonicalPath(p); c != "" {
		return c
	}
	return filepath.Clean(p)
}

func parseSSHURLTarget(raw string) (string, uint16) {
	trimmed := raw[len("ssh://"):]
	if slash := strings.IndexByte(trimmed, '/'); slash >= 0 {
		trimmed = trimmed[:slash]
	}
	if at := strings.LastIndexByte(trimmed, '@'); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	if host, portStr, err := net.SplitHostPort(trimmed); err == nil {
		if p, pErr := strconv.ParseUint(portStr, 10, 16); pErr == nil && p > 0 {
			return strings.ToLower(strings.Trim(host, "[]")), uint16(p)
		}
	}
	return strings.ToLower(strings.Trim(trimmed, "[]")), 0
}
