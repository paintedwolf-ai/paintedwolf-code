package confine

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fspath"
)

// MaxSocketGrants is the hard cap on applied AF_UNIX literals per execution.
const MaxSocketGrants = 8

// maxSocketPathBytes is the per-path length bound for approved/resolved socket paths.
const maxSocketPathBytes = 1024

// EgressRequest is the host-derived network axis.
type EgressRequest uint8

const (
	// EgressDefault selects mediated proxy mode when the broker is healthy, else NetworkDeny.
	EgressDefault EgressRequest = iota
	// EgressDeny selects NetworkDeny (no public outbound).
	EgressDeny
	// EgressDirectIP permits IP networking and independent socket grants.
	EgressDirectIP
)

// SocketGrant is one host-approved exact AF_UNIX connect literal.
type SocketGrant struct {
	ApprovedPath string
	ResolvedPath string
}

// SocketGrantDropReason is a stable machine reason a grant was not applied.
type SocketGrantDropReason string

const (
	SocketDropNotAbsolute   SocketGrantDropReason = "not_absolute"
	SocketDropInvalidUTF8   SocketGrantDropReason = "invalid_utf8"
	SocketDropNUL           SocketGrantDropReason = "nul"
	SocketDropTooLong       SocketGrantDropReason = "too_long"
	SocketDropResolveFailed SocketGrantDropReason = "resolve_failed"
	SocketDropRepointed     SocketGrantDropReason = "repointed"
	SocketDropNotSocket     SocketGrantDropReason = "not_socket"
)

// SocketGrantDrop records a grant that was refused or dropped at the choke point.
type SocketGrantDrop struct {
	Grant  SocketGrant
	Reason SocketGrantDropReason
}

// Request is the intentional boundary input for DefaultConfinement.
type Request struct {
	ProcessControl bool
	HostExecution  bool
	// ProjectID is an opaque write-root routing key.
	ProjectID string
	// Roots are attached jail identity (project trees and draft scratch).
	Roots []string
	// SessionScratchRoot is the session-owned scratch root for this execution.
	SessionScratchRoot string
	// GrantedWriteRoots are reviewed write-root leases for this action. They
	// expand the write profile without becoming attached jail identity.
	GrantedWriteRoots []string
	ReadRoots         []string
	// ReadDenyPaths are host-resolved per-action exclusions.
	ReadDenyPaths []string
	SocketGrants  []SocketGrant
	// ProtectedWriteGrants are exact protected-path write exceptions.
	ProtectedWriteGrants []ProtectedPathGrant
	// PolicyWriteGrants expire with this invocation and cover reviewed
	// agent-policy files or one loader tree.
	PolicyWriteGrants []ProtectedPathGrant
	// ProtectedReadGrants are exact protected-path read exceptions.
	ProtectedReadGrants []ProtectedPathGrant
	Egress              EgressRequest
	// DirectIPDeclared narrows direct IP to declared transports.
	DirectIPDeclared []string
	// SocksProxyEnv injects the mediated TCP endpoint.
	SocksProxyEnv bool
	// LocalListen grants local listener authority without widening egress.
	LocalListen bool
	// LocalListenPorts narrows the listener grant; empty means any local port.
	LocalListenPorts []uint16
	// LoopbackConnect permits local outbound connections.
	LoopbackConnect bool
	// LoopbackConnectPorts optionally limits local destination ports.
	LoopbackConnectPorts []uint16
}

// ErrSocketGrantConflict is returned when two grants share an ApprovedPath but disagree
// on ResolvedPath after normalization.
var ErrSocketGrantConflict = errors.New("confine: conflicting socket grants for the same approved path")

// ErrSocketGrantCap is returned when more than MaxSocketGrants grants would apply.
var ErrSocketGrantCap = errors.New("confine: socket grant cap exceeded")

// preparedRequest is the choke-point result: sorted roots, validated applied grants, drops.
type preparedRequest struct {
	Roots []string
	// SessionScratchRoot is the session-owned scratch root for this execution.
	SessionScratchRoot string
	// Validate every granted write root at the confinement boundary.
	GrantedWriteRoots     []string
	ReadRoots             []string
	ReadDenyPaths         []string
	SocketGrants          []SocketGrant
	Dropped               []SocketGrantDrop
	ProtectedWrites       []ProtectedPathGrant
	PolicyWrites          []ProtectedPathGrant
	DroppedProtected      []ProtectedPathDrop
	ProtectedReads        []ProtectedPathGrant
	DroppedProtectedReads []ProtectedPathDrop
	Egress                EgressRequest
	DirectIPPermits       []DirectIPPermit
	LocalListen           bool
	LocalListenPorts      []uint16
	LoopbackConnect       bool
	LoopbackConnectPorts  []uint16
}

func prepareRequest(req Request) (preparedRequest, error) {
	roots := normalizePathList(req.Roots)
	if err := ValidateAttachedWriteRoots(roots); err != nil {
		return preparedRequest{}, err
	}
	grantedWriteRoots := normalizePathList(req.GrantedWriteRoots)
	if err := ValidateGrantedWriteRoots(grantedWriteRoots); err != nil {
		return preparedRequest{}, err
	}
	readRoots := normalizePathList(req.ReadRoots)
	readDenyPaths := normalizePathList(req.ReadDenyPaths)
	grants, err := normalizeSocketGrantList(req.SocketGrants)
	if err != nil {
		return preparedRequest{}, err
	}
	applied, dropped, err := validateSocketGrants(grants)
	if err != nil {
		return preparedRequest{}, err
	}
	protected, droppedProtected, err := validateProtectedPathGrants(
		normalizeProtectedPathGrants(req.ProtectedWriteGrants),
	)
	if err != nil {
		return preparedRequest{}, err
	}
	protectedReads, droppedReads, err := validateProtectedPathGrants(
		normalizeProtectedPathGrants(req.ProtectedReadGrants),
	)
	if err != nil {
		return preparedRequest{}, err
	}
	policyWrites, err := ValidatePolicyWriteGrants(req.PolicyWriteGrants, roots)
	if err != nil {
		return preparedRequest{}, err
	}
	// Only direct-IP requests consume transport declarations.
	var permits []DirectIPPermit
	if req.Egress == EgressDirectIP {
		permits, _ = ParseDirectIPPermits(req.DirectIPDeclared)
	}
	var sessionScratchRoot string
	if trimmed := strings.TrimSpace(req.SessionScratchRoot); trimmed != "" {
		sessionScratchRoot = filepath.Clean(trimmed)
	}
	return preparedRequest{
		Roots:                 roots,
		SessionScratchRoot:    sessionScratchRoot,
		GrantedWriteRoots:     grantedWriteRoots,
		ReadRoots:             readRoots,
		ReadDenyPaths:         readDenyPaths,
		SocketGrants:          applied,
		Dropped:               dropped,
		ProtectedWrites:       protected,
		PolicyWrites:          policyWrites,
		DroppedProtected:      droppedProtected,
		ProtectedReads:        protectedReads,
		DroppedProtectedReads: droppedReads,
		Egress:                req.Egress,
		DirectIPPermits:       permits,
		LocalListen:           req.LocalListen,
		LocalListenPorts:      normalizeListenPorts(req.LocalListenPorts),
		LoopbackConnect:       req.LoopbackConnect,
		LoopbackConnectPorts:  normalizeListenPorts(req.LoopbackConnectPorts),
	}, nil
}

// normalizeListenPorts sorts, dedups, and drops zero ports.
func normalizeListenPorts(in []uint16) []uint16 {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[uint16]struct{}, len(in))
	out := make([]uint16, 0, len(in))
	for _, p := range in {
		if p == 0 {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func normalizePathList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func normalizeSocketGrantList(in []SocketGrant) ([]SocketGrant, error) {
	if len(in) == 0 {
		return nil, nil
	}
	byApproved := map[string]string{}
	order := make([]string, 0, len(in))
	for _, g := range in {
		approved := strings.TrimSpace(g.ApprovedPath)
		resolved := strings.TrimSpace(g.ResolvedPath)
		if approved == "" && resolved == "" {
			continue
		}
		if prev, ok := byApproved[approved]; ok {
			if prev != resolved {
				return nil, fmt.Errorf("%w: %q", ErrSocketGrantConflict, approved)
			}
			continue
		}
		byApproved[approved] = resolved
		order = append(order, approved)
	}
	sort.Strings(order)
	out := make([]SocketGrant, 0, len(order))
	for _, approved := range order {
		out = append(out, SocketGrant{ApprovedPath: approved, ResolvedPath: byApproved[approved]})
	}
	return out, nil
}

func validateSocketGrants(grants []SocketGrant) ([]SocketGrant, []SocketGrantDrop, error) {
	if len(grants) == 0 {
		return nil, nil, nil
	}
	applied := make([]SocketGrant, 0, len(grants))
	var dropped []SocketGrantDrop
	for _, g := range grants {
		if reason := socketPathSyntaxReason(g.ApprovedPath); reason != "" {
			dropped = append(dropped, SocketGrantDrop{Grant: g, Reason: reason})
			continue
		}
		if reason := socketPathSyntaxReason(g.ResolvedPath); reason != "" {
			dropped = append(dropped, SocketGrantDrop{Grant: g, Reason: reason})
			continue
		}
		now := fspath.CanonicalPath(g.ApprovedPath)
		if now == "" {
			dropped = append(dropped, SocketGrantDrop{Grant: g, Reason: SocketDropResolveFailed})
			continue
		}
		if now != g.ResolvedPath {
			dropped = append(dropped, SocketGrantDrop{Grant: g, Reason: SocketDropRepointed})
			continue
		}
		fi, err := os.Lstat(now)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			dropped = append(dropped, SocketGrantDrop{Grant: g, Reason: SocketDropNotSocket})
			continue
		}
		applied = append(applied, SocketGrant{ApprovedPath: g.ApprovedPath, ResolvedPath: now})
	}
	if len(applied) > MaxSocketGrants {
		return nil, nil, fmt.Errorf("%w: %d > %d", ErrSocketGrantCap, len(applied), MaxSocketGrants)
	}
	return applied, dropped, nil
}

func socketPathSyntaxReason(p string) SocketGrantDropReason {
	if p == "" || !filepath.IsAbs(p) {
		return SocketDropNotAbsolute
	}
	if strings.ContainsRune(p, 0) {
		return SocketDropNUL
	}
	if !utf8.ValidString(p) {
		return SocketDropInvalidUTF8
	}
	if len(p) > maxSocketPathBytes {
		return SocketDropTooLong
	}
	return ""
}

// SocketPathsDigest is a redaction-safe digest of applied approved/resolved pairs.
func SocketPathsDigest(grants []SocketGrant) string {
	if len(grants) == 0 {
		return ""
	}
	parts := make([]string, 0, len(grants))
	for _, g := range grants {
		parts = append(parts, g.ApprovedPath+"\x00"+g.ResolvedPath)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
