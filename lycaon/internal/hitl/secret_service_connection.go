package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A chat connection grant with no port filter covers every local port.
func uncoveredServicePorts(ports []uint16, authority []ApprovalAuthorityDelta, chatSessionID string) []uint16 {
	missing := append([]uint16(nil), ports...)
	for _, delta := range authority {
		if delta.Kind != AuthorityLoopbackConnectChat || delta.ChatSessionID != chatSessionID {
			continue
		}
		if len(delta.ConnectPorts) == 0 {
			return nil
		}
		missing = slices.DeleteFunc(missing, func(port uint16) bool {
			return slices.Contains(delta.ConnectPorts, port)
		})
	}
	return missing
}

// secretServiceConnection carries the exact local ports named alongside setup.
func secretServiceConnection(action ProposedAction, permission *SecretPermission, option ApprovalOption) ApprovalAuthorityDelta {
	parts := make([]string, 0, len(permission.ConnectPorts))
	for _, port := range permission.ConnectPorts {
		parts = append(parts, strconv.Itoa(int(port)))
	}
	ports := strings.Join(parts, ", ")
	key := strings.Join([]string{action.Scope.ProjectID, action.Scope.ChatSession(), ports, string(option.Rung)}, "\x00")
	grant := ApprovalGrant{
		ID: serviceConnectionID(key), Scope: ApprovalGrantScopeChat, ChatSessionID: action.Scope.ChatSession(),
		ProjectID: action.Scope.ProjectID, ProjectDir: action.Scope.ProjectDir,
		Predicate: ApprovalGrantPredicate{Category: ApprovalGrantCategoryLoopbackConnect, Pattern: ports},
		Title:     "Connect to approved local services", Coverage: "local connections on ports " + ports,
		GrantedAt: time.Now().UTC(), ExpiresWhen: ExpiresWhenChatDeleted, ReaskWhen: "a different local port is needed",
		Source: "checkpoint",
	}
	ttl := 0
	if option.Rung == ApprovalRungDay {
		ttl = DayRungTTLSeconds
	}
	return ApprovalAuthorityDelta{Kind: AuthorityLoopbackConnectChat, ChatSessionID: action.Scope.ChatSession(),
		Grant: &grant, ConnectPorts: append([]uint16(nil), permission.ConnectPorts...), TTLSeconds: ttl}
}

func secretServiceConnectionTarget(ports []uint16) ApprovalTarget {
	return ApprovalTarget{Kind: "loopback_connect", Label: "Local connections on ports " + servicePortLabel(ports),
		Details: map[string]any{"connect_ports": ports}}
}

func serviceConnectionID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "grant_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func servicePortLabel(ports []uint16) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(int(port))
	}
	return strings.Join(parts, ", ")
}

// Local connection authority lasts for the chat, independently of secret permission expiry.
func attachSecretServiceConnections(action ProposedAction, screen *SecretScreen, options []ApprovalOption) []ApprovalOption {
	if screen == nil || len(screen.ConnectPorts) == 0 {
		return options
	}
	permission := &SecretPermission{Screen: *screen, ConnectPorts: screen.ConnectPorts}
	for i := range options {
		option := &options[i]
		if option.Disabled || option.Group != "" || option.Kind == ApprovalOptionCurrentAction || option.DecisionAction != ApprovalOptionApprove {
			continue
		}
		option.Authority = append(append([]ApprovalAuthorityDelta(nil), option.Authority...), secretServiceConnection(action, permission, *option))
		option.Coverage += "; local connections on ports " + servicePortLabel(screen.ConnectPorts) + " until this chat is deleted"
		if option.Rung == ApprovalRungDay {
			option.Coverage += " or one day passes"
		}
	}
	return options
}
