package capabilityadmin

import (
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func (s *Handler) installChatApprovalDelta(checkpointID string, delta hitl.ApprovalAuthorityDelta) (func(), error) {
	chatSessionID := delta.ChatSession()
	switch delta.Kind {
	case hitl.AuthoritySocketChat:
		if s.Sockets == nil || delta.Grant == nil {
			return nil, fmt.Errorf("socket task authority is not configured")
		}
		if _, root, exists := s.Sockets.FindByID(delta.Grant.ID); exists && root != chatSessionID {
			return nil, fmt.Errorf("socket task authority id is already installed for another task")
		}
		already, live, withID := socketGrantSetByID(s.Sockets.ListChatGrants(chatSessionID), delta.Grant.ID, delta.Sockets)
		if withID > 0 {
			if already != len(delta.Sockets) || withID != already {
				return nil, fmt.Errorf("socket task authority id is already installed with different targets")
			}
			if live == already {
				return nil, nil
			}
		}
		created := false
		for _, target := range delta.Sockets {
			created = s.Sockets.GrantChat(chatSessionID, confine.SocketGrant{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath}, delta.Grant.ID, checkpointID, delta.ActionDigest, chatExpiry(delta.TTLSeconds)) || created
		}
		if !created {
			return nil, nil
		}
		return func() { _, _ = s.Sockets.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID) }, nil
	case hitl.AuthorityDirectIPChat:
		if s.DirectIP == nil || delta.DirectIPLease == nil || delta.Grant == nil {
			return nil, fmt.Errorf("direct-IP task authority is not configured")
		}
		if existing, root, exists := s.DirectIP.FindByID(delta.Grant.ID); exists {
			if root != chatSessionID || !directIPGrantMatches(existing, *delta.DirectIPLease) {
				return nil, fmt.Errorf("direct-IP task authority id is already installed for another action")
			}
			if existing.ExpiresAt == nil || existing.ExpiresAt.After(time.Now().UTC()) {
				return nil, nil
			}
		}
		if !s.DirectIP.GrantChat(chatSessionID, *delta.DirectIPLease, delta.Grant.ID, checkpointID, chatExpiry(delta.TTLSeconds)) {
			return nil, nil
		}
		return func() { _, _ = s.DirectIP.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID) }, nil
	case hitl.AuthorityWriteRootChat:
		if s.WriteRoots == nil || delta.Grant == nil {
			return nil, fmt.Errorf("write-root approval authority is not configured")
		}
		if existing, root, exists := s.WriteRoots.FindByID(delta.Grant.ID); exists {
			if root != chatSessionID || !writeRootGrantSetMatches(
				s.WriteRoots.ListChatGrants(chatSessionID), delta.Grant.ID, delta.WriteRoots,
			) {
				return nil, fmt.Errorf("write-root task authority id is already installed with different roots")
			}
			if existing.Live(time.Now().UTC()) {
				return nil, nil
			}
		}
		created := false
		for _, root := range delta.WriteRoots {
			created = s.WriteRoots.GrantChat(chatSessionID, root, delta.Grant.ID, checkpointID, chatExpiry(delta.TTLSeconds)) || created
		}
		if !created {
			return nil, nil
		}
		return func() { _, _ = s.WriteRoots.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID) }, nil
	case hitl.AuthorityReadPathChat:
		if s.ReadPaths == nil || delta.Grant == nil {
			return nil, fmt.Errorf("read-path approval authority is not configured")
		}
		if _, root, exists := s.ReadPaths.FindByID(delta.Grant.ID); exists {
			if root != chatSessionID || !writeRootGrantSetMatches(
				s.ReadPaths.ListChatGrants(chatSessionID), delta.Grant.ID, delta.ReadPaths,
			) {
				return nil, fmt.Errorf("read-path task authority id is already installed with different paths")
			}
			return nil, nil
		}
		created := false
		for _, path := range delta.ReadPaths {
			created = s.ReadPaths.GrantChat(chatSessionID, path, delta.Grant.ID, checkpointID, chatExpiry(delta.TTLSeconds)) || created
		}
		if !created {
			return nil, nil
		}
		return func() { _, _ = s.ReadPaths.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID) }, nil
	case hitl.AuthorityLocalListenChat:
		if s.Listen == nil || delta.Grant == nil {
			return nil, fmt.Errorf("local-listen approval authority is not configured")
		}
		if existing, root, exists := s.Listen.FindByID(delta.Grant.ID); exists {
			if root != chatSessionID || approvalstate.PortGrantKey(existing.Ports) != approvalstate.PortGrantKey(delta.ListenPorts) {
				return nil, fmt.Errorf("local-listen task authority id is already installed for other ports")
			}
			if existing.ExpiresAt == nil || existing.ExpiresAt.After(time.Now().UTC()) {
				return nil, nil
			}
		}
		if !s.Listen.GrantChat(chatSessionID, delta.ListenPorts, delta.Grant.ID, checkpointID, chatExpiry(delta.TTLSeconds)) {
			return nil, nil
		}
		grantID := delta.Grant.ID
		return func() { _, _ = s.Listen.RevokeByIDInstalledBy(grantID, checkpointID) }, nil
	case hitl.AuthorityLoopbackConnectChat:
		if s.Loopback == nil || delta.Grant == nil {
			return nil, fmt.Errorf("loopback-connect approval authority is not configured")
		}
		if existing, root, exists := s.Loopback.FindByID(delta.Grant.ID); exists {
			if root != chatSessionID || approvalstate.PortGrantKey(existing.Ports) != approvalstate.PortGrantKey(delta.ConnectPorts) {
				return nil, fmt.Errorf("loopback-connect task authority id is already installed for other ports")
			}
			if existing.ExpiresAt == nil || existing.ExpiresAt.After(time.Now().UTC()) {
				return nil, nil
			}
		}
		if !s.Loopback.GrantChat(chatSessionID, delta.ConnectPorts, delta.Grant.ID, checkpointID, chatExpiry(delta.TTLSeconds)) {
			return nil, nil
		}
		grantID := delta.Grant.ID
		return func() { _, _ = s.Loopback.RevokeByIDInstalledBy(grantID, checkpointID) }, nil
	default:
		return nil, fmt.Errorf("unsupported task approval authority delta %q", delta.Kind)
	}
}

func socketGrantSetByID(existing []approvalstate.SocketChatGrant, id string, targets []hitl.ApprovalSocketTarget) (covered, live, withID int) {
	pairs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		pairs[target.ApprovedPath+"\x00"+target.ResolvedPath] = struct{}{}
	}
	for _, grant := range existing {
		if grant.ID != id {
			continue
		}
		withID++
		if _, ok := pairs[grant.ApprovedPath+"\x00"+grant.ResolvedPath]; ok {
			covered++
			if grant.ExpiresAt == nil || grant.ExpiresAt.After(time.Now().UTC()) {
				live++
			}
		}
	}
	return covered, live, withID
}

func directIPGrantMatches(existing approvalstate.DirectIPChatGrant, requested hitl.DirectIPLease) bool {
	if existing.ChatConfinementDigest != "" && requested.ChatConfinementDigest != "" {
		return existing.ChatConfinementDigest == strings.TrimSpace(requested.ChatConfinementDigest)
	}
	return existing.ActionDigest == strings.TrimSpace(requested.ActionDigest) &&
		existing.RequestDigest == strings.TrimSpace(requested.RequestDigest) &&
		existing.ConfinementDigest == strings.TrimSpace(requested.ConfinementDigest)
}

func writeRootGrantSetMatches(existing []approvalstate.PathChatGrant, id string, requested []string) bool {
	want := make(map[string]struct{}, len(requested))
	for _, root := range requested {
		if normalized := confine.NormalizeWriteRootKey(root); normalized != "" {
			want[normalized] = struct{}{}
		}
	}
	got := map[string]struct{}{}
	for _, grant := range existing {
		if grant.ID == id {
			got[grant.Root] = struct{}{}
		}
	}
	if len(got) != len(want) {
		return false
	}
	for root := range want {
		if _, exists := got[root]; !exists {
			return false
		}
	}
	return true
}

func chatExpiry(ttlSeconds int) *time.Time {
	if ttlSeconds <= 0 {
		return nil
	}
	expires := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second)
	return &expires
}
