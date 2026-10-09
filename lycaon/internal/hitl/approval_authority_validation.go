package hitl

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
	"path/filepath"
	"slices"
	"strings"
)

func (o ApprovalOption) validateGrantIdentities() error {
	// Disabled placeholders do not require a durable grant identity.
	if o.Disabled {
		return nil
	}
	for _, delta := range o.Authority {
		if delta.Grant == nil {
			continue
		}
		if err := delta.Grant.ValidateDurableIdentity(); err != nil {
			return err
		}
	}
	return nil
}

func (o ApprovalOption) validateKind() error {
	reusable := false
	for _, delta := range o.Authority {
		switch delta.Kind {
		case AuthorityGenericGrant, AuthoritySocketChat, AuthorityDirectIPChat, AuthorityWriteRootChat,
			AuthorityReadPathChat, AuthorityLocalListenChat, AuthorityLoopbackConnectChat, AuthorityGrantedPath,
			AuthorityTrustDestination:
			reusable = true
		case AuthorityCurrentAction, AuthoritySocketPermit, AuthorityDirectIPPermit, AuthorityAskQuiet:
		}
	}
	switch o.Kind {
	case ApprovalOptionCurrentAction:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("current-action option has invalid scope or decision")
		}
		if o.Rung != ApprovalRungOnce && o.Rung != ApprovalRungUnchanged {
			return fmt.Errorf("current-action option has invalid rung %q", o.Rung)
		}
		if reusable {
			return fmt.Errorf("current-action option contains reusable authority")
		}
	case ApprovalOptionLease:
		if o.Scope != ApprovalGrantScopeChat && o.Scope != ApprovalGrantScopeProject && o.Scope != ApprovalGrantScopeDevice {
			return fmt.Errorf("lease option has invalid scope")
		}
		if o.Rung != ApprovalRungDay && o.Rung != ApprovalRungChat && o.Rung != ApprovalRungProject && o.Rung != ApprovalRungDevice {
			return fmt.Errorf("lease option has invalid rung %q", o.Rung)
		}
		if o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("lease option has invalid decision")
		}
		if !reusable {
			return fmt.Errorf("lease option has no reusable authority")
		}
	case ApprovalOptionRedacted:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionRedact {
			return fmt.Errorf("redaction option has invalid scope or decision")
		}
		if o.Rung != ApprovalRungRedacted {
			return fmt.Errorf("redaction option has invalid rung %q", o.Rung)
		}
		if reusable {
			return fmt.Errorf("redaction option contains reusable authority")
		}
	case ApprovalOptionTracked:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionTrack || o.Rung != ApprovalRungTracked {
			return fmt.Errorf("tracked option has invalid scope, decision, or rung")
		}
		if reusable {
			return fmt.Errorf("tracked option contains reusable authority")
		}
	case ApprovalOptionQuiet:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("quiet option has invalid scope or decision")
		}
		// The quiet slot is one row: the rest of this chat.
		if o.Rung != ApprovalRungChat {
			return fmt.Errorf("quiet option has invalid rung %q", o.Rung)
		}
		hasQuiet := false
		for _, delta := range o.Authority {
			if delta.Kind == AuthorityAskQuiet {
				hasQuiet = true
			}
			// A quiet title states one day or the rest of this chat, so it may only
			// carry authority bounded the same way.
			if delta.Grant != nil && !delta.Grant.TimeBounded() {
				return fmt.Errorf("quiet option carries standing %s authority behind chat-bounded copy", delta.Grant.Scope)
			}
		}
		if !hasQuiet {
			return fmt.Errorf("quiet option has no ask_quiet authority")
		}
	default:
		return fmt.Errorf("unknown option kind %q", o.Kind)
	}
	return nil
}

func (d ApprovalAuthorityDelta) validate() error {
	if d.Grant != nil {
		if err := ValidateElevatedEffects(d.Grant.ElevatedEffects); err != nil {
			return err
		}
	}
	if d.AskQuiet != nil {
		if err := ValidateElevatedEffects(d.AskQuiet.ElevatedEffects); err != nil {
			return err
		}
	}
	if d.Kind != AuthorityLocalListenChat && len(d.ListenPorts) != 0 {
		return fmt.Errorf("authority %q carries unrelated listen ports", d.Kind)
	}
	if d.Kind != AuthorityLoopbackConnectChat && len(d.ConnectPorts) != 0 {
		return fmt.Errorf("authority %q carries unrelated connect ports", d.Kind)
	}
	switch d.Kind {
	case AuthorityCurrentAction:
		if d.hasGrant() || d.hasBoundaryFields() || d.TTLSeconds != 0 {
			return fmt.Errorf("current-action authority has unrelated fields")
		}
	case AuthorityGenericGrant:
		if !d.hasGrant() {
			return fmt.Errorf("generic grant is missing")
		}
		if d.ChatSessionID != "" || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("generic grant has unrelated fields")
		}
	case AuthoritySocketPermit, AuthoritySocketChat:
		if strings.TrimSpace(d.ActionDigest) == "" || len(d.Sockets) == 0 {
			return fmt.Errorf("socket authority is incomplete")
		}
		if d.Kind == AuthoritySocketPermit {
			if d.hasGrant() || strings.TrimSpace(d.SessionID) == "" || strings.TrimSpace(d.ToolCallID) == "" || d.ChatSessionID != "" || d.DirectIPLease != nil || len(d.WriteRoots) != 0 || d.TTLSeconds != 0 {
				return fmt.Errorf("socket permit fields are invalid")
			}
		} else if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" || d.SessionID != "" || d.ToolCallID != "" || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("socket chat fields are invalid")
		}
	case AuthorityDirectIPPermit, AuthorityDirectIPChat:
		if d.DirectIPLease == nil || !d.DirectIPLease.Complete() {
			return fmt.Errorf("direct-IP authority is incomplete")
		}
		if d.Kind == AuthorityDirectIPPermit {
			if d.hasGrant() || strings.TrimSpace(d.SessionID) == "" || strings.TrimSpace(d.ToolCallID) == "" || d.ActionDigest != d.DirectIPLease.ActionDigest || d.ChatSessionID != "" || len(d.Sockets) != 0 || len(d.WriteRoots) != 0 || d.TTLSeconds != 0 {
				return fmt.Errorf("direct-IP permit fields are invalid")
			}
		} else if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || len(d.WriteRoots) != 0 {
			return fmt.Errorf("direct-IP chat fields are invalid")
		}
	case AuthorityWriteRootChat:
		if !d.hasGrant() ||
			strings.TrimSpace(d.ChatSession()) == "" || len(d.WriteRoots) == 0 {
			return fmt.Errorf("write-root authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.ReadPaths) != 0 {
			return fmt.Errorf("write-root authority has unrelated fields")
		}
	case AuthorityReadPathChat:
		if !d.hasGrant() ||
			strings.TrimSpace(d.ChatSession()) == "" || len(d.ReadPaths) == 0 {
			return fmt.Errorf("read-path authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || d.TTLSeconds != 0 || len(d.WriteRoots) != 0 {
			return fmt.Errorf("read-path authority has unrelated fields")
		}
	case AuthorityLocalListenChat:
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("local-listen authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 ||
			len(d.WriteRoots) != 0 || d.DirectIPLease != nil {
			return fmt.Errorf("local-listen authority has unrelated fields")
		}
		for _, port := range d.ListenPorts {
			if port == 0 {
				return fmt.Errorf("local-listen authority names port 0")
			}
		}
	case AuthorityLoopbackConnectChat:
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("loopback-connect authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 ||
			len(d.WriteRoots) != 0 || d.DirectIPLease != nil || len(d.ListenPorts) != 0 {
			return fmt.Errorf("loopback-connect authority has unrelated fields")
		}
		for _, port := range d.ConnectPorts {
			if port == 0 {
				return fmt.Errorf("loopback-connect authority names port 0")
			}
		}
	case AuthorityGrantedPath:
		if d.GrantedPath == nil || strings.TrimSpace(d.GrantedPath.Path) == "" ||
			!filepath.IsAbs(d.GrantedPath.Path) {
			return fmt.Errorf("granted-path authority needs an absolute path")
		}
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("granted-path authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" ||
			len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("granted-path authority has unrelated fields")
		}
	case AuthorityTrustDestination:
		if d.TrustDestination == nil || strings.TrimSpace(d.TrustDestination.ProviderID) == "" ||
			strings.TrimSpace(d.TrustDestination.DestinationID) == "" || strings.TrimSpace(d.TrustDestination.Label) == "" {
			return fmt.Errorf("trust-destination authority is incomplete")
		}
		if !d.hasGrant() || d.hasBoundaryFields() || d.GrantedPath != nil || d.AskQuiet != nil || d.TTLSeconds != 0 {
			return fmt.Errorf("trust-destination authority has unrelated fields")
		}
	case AuthorityAskQuiet:
		if d.AskQuiet == nil || strings.TrimSpace(d.AskQuiet.ID) == "" ||
			strings.TrimSpace(d.AskQuiet.Key) == "" || strings.TrimSpace(d.AskQuiet.Label) == "" {
			return fmt.Errorf("ask-quiet authority is incomplete")
		}
		if strings.TrimSpace(d.ChatSessionID) == "" {
			return fmt.Errorf("ask-quiet authority needs a chat session")
		}
		if d.hasGrant() || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" ||
			len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 || d.GrantedPath != nil {
			return fmt.Errorf("ask-quiet authority has unrelated fields")
		}
		if d.TTLSeconds < 0 {
			return fmt.Errorf("ask-quiet authority has invalid ttl")
		}
	default:
		return fmt.Errorf("unknown authority delta %q", d.Kind)
	}
	return nil
}

func (d ApprovalAuthorityDelta) hasGrant() bool {
	return d.Grant != nil && strings.TrimSpace(d.Grant.ID) != ""
}

func (d ApprovalAuthorityDelta) hasBoundaryFields() bool {
	return d.ChatSessionID != "" || d.SessionID != "" || d.ToolCallID != "" ||
		d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 ||
		len(d.ListenPorts) != 0 || len(d.ConnectPorts) != 0
}

// approvalDirectoryScopes preserves host candidate order before ladder sorting.
func approvalDirectoryScopes(options []ApprovalOption) []string {
	var paths []string
	for _, option := range options {
		if option.DirectoryScope != "" && !slices.Contains(paths, option.DirectoryScope) {
			paths = append(paths, option.DirectoryScope)
		}
	}
	return paths
}

// validateDirectoryScopes binds every presentation candidate to its exact offered authority.
func (p *ApprovalPlan) validateDirectoryScopes() error {
	for index, path := range p.DirectoryScopes {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || (index > 0 && !confine.PathStrictlyUnder(p.DirectoryScopes[index-1], path)) {
			return fmt.Errorf("directory scopes must be canonical ancestors")
		}
	}
	for _, option := range p.Options {
		if option.DirectoryScope == "" {
			continue
		}
		if !slices.Contains(p.DirectoryScopes, option.DirectoryScope) {
			return fmt.Errorf("option %q names an unoffered directory", option.ID)
		}
		bound := false
		for _, delta := range option.Authority {
			for _, access := range []*GrantedPathDelta{delta.GrantedPath, directoryLeasePath(delta.Grant)} {
				if access != nil && (!access.Tree || access.Write || access.Path != option.DirectoryScope) {
					return fmt.Errorf("option %q directory differs from its authority", option.ID)
				}
			}
			if delta.Kind == AuthorityGrantedPath && delta.GrantedPath != nil && delta.GrantedPath.Tree && !delta.GrantedPath.Write && delta.GrantedPath.Path == option.DirectoryScope {
				bound = true
			}
		}
		if !bound {
			return fmt.Errorf("option %q directory differs from its authority", option.ID)
		}
	}
	return nil
}

func directoryLeasePath(grant *ApprovalGrant) *GrantedPathDelta {
	if grant == nil {
		return nil
	}
	return grant.GrantedPath
}
