package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func (g *RuleApprovalGate) currentActionFileAccess(action hitl.ProposedAction) []hitl.GrantedPathDelta {
	var access []hitl.GrantedPathDelta
	for _, target := range g.declaredFileTargets(action) {
		// Relative escapes remain invalid tool arguments, not grant subjects.
		if !target.OutsideRoots || !filepath.IsAbs(target.Path) {
			continue
		}
		norm := grantedpath.Normalize(target.Path)
		write := target.Mode == gate.ModeWrite
		access = append(access, hitl.GrantedPathDelta{
			Path:      norm,
			Write:     write,
			WriteOnly: write && confine.SharedTempLocation(norm) != "",
		})
	}
	return access
}

// Current-action authority covers the same exact directional subject during
// prepared effect review, without becoming a reusable grant or covering other gates.
func currentActionCoversTarget(action hitl.ProposedAction, target *gate.FileTarget) bool {
	path := grantedpath.Normalize(target.Path)
	for _, access := range action.Invocation.ApprovedFileAccess {
		if access.Path == path && (!access.Tree) && access.Permits(target.Mode == gate.ModeWrite) {
			return true
		}
	}
	return false
}

func (g *RuleApprovalGate) fileAccessCoversTargets(durable []ApprovalGrant, action hitl.ProposedAction) bool {
	targets := g.declaredFileTargets(action)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !currentActionCoversTarget(action, target) && !grantedPathCoversTarget(g, durable, action, target) {
			return false
		}
	}
	return true
}

func isSharedTempCrossing(target gate.FileTarget) bool {
	if !target.OutsideRoots || target.ProtectedSubject || target.Sensitive {
		return false
	}
	probe := strings.TrimSpace(target.ResolvedPath)
	if probe == "" {
		probe = strings.TrimSpace(target.Path)
	}
	if !filepath.IsAbs(probe) {
		return false
	}
	return confine.SharedTempLocation(probe) != ""
}

func allSharedTempCrossings(targets []*gate.FileTarget) bool {
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if target == nil || !isSharedTempCrossing(*target) {
			return false
		}
	}
	return true
}

func sharedTempTargetAccess(action hitl.ProposedAction, target gate.FileTarget) (hitl.GrantedPathDelta, bool, bool) {
	probe := strings.TrimSpace(target.ResolvedPath)
	if probe == "" {
		probe = strings.TrimSpace(target.Path)
	}
	canonical := grantedpath.Normalize(probe)
	if canonical == "" || !filepath.IsAbs(canonical) {
		return hitl.GrantedPathDelta{}, false, false
	}
	_, exists, isDir := probeTargetFolder(canonical)
	if target.Exists != nil && !*target.Exists {
		exists, isDir = false, false
	}
	if target.Mode == gate.ModeWrite {
		tree := action.Invocation.Tool == "extract_archive" || (exists && isDir)
		return hitl.GrantedPathDelta{Path: canonical, Write: true, WriteOnly: true, Tree: tree}, isDir, true
	}
	return hitl.GrantedPathDelta{Path: canonical, Write: false, Tree: exists && isDir}, exists && isDir, true
}

func sharedTempGrantedPathOffers(action hitl.ProposedAction, target gate.FileTarget, decision *gate.Decision) []hitl.ApprovalGrantOffer {
	if decision == nil || decision.Reuse().Scope == gate.ScopeNone || slices.Contains(decision.Gates(), api.GateSecretOutbound) {
		return nil
	}
	access, isDir, ok := sharedTempTargetAccess(action, target)
	if !ok {
		return nil
	}
	primary := sharedTempSingleChatOffer(action, access, hitl.TitleAllowForThisChat, isDir && !access.Write)
	offers := []hitl.ApprovalGrantOffer{primary}
	if access.Write {
		if exchangeDir := sharedTempContainerDir(access.Path); exchangeDir != "" {
			offers = append(offers, hitl.SharedTempExchangeFolderOffer(action, exchangeDir, isSharedTempTopRoot(exchangeDir)))
		}
		return offers
	}
	if isDir {
		primary.DirectoryScope = access.Path
		offers[0] = primary
		offers = append(offers, hitl.SharedTempExchangeFolderOffer(action, access.Path, isSharedTempTopRoot(access.Path)))
		return offers
	}
	parentDir := filepath.Dir(access.Path)
	if confine.SharedTempLocation(parentDir) != "" {
		if _, pExists, pDir := probeTargetFolder(parentDir); pExists && pDir {
			dirAccess := hitl.GrantedPathDelta{Path: parentDir, Write: false, Tree: true}
			dirOffer := sharedTempSingleChatOffer(action, dirAccess, "Read and list "+parentDir+" for this chat", true)
			dirOffer.Group = hitl.GroupAlsoAllow
			dirOffer.DirectoryScope = parentDir
			offers = append(offers, dirOffer, hitl.SharedTempExchangeFolderOffer(action, parentDir, isSharedTempTopRoot(parentDir)))
		}
	}
	return offers
}

// MultiSharedTempGrantedPathOffers authorizes all uncovered shared-temp crossings on one card.
func MultiSharedTempGrantedPathOffers(action hitl.ProposedAction, targets []*gate.FileTarget, decision *gate.Decision) []hitl.ApprovalGrantOffer {
	if decision == nil || decision.Reuse().Scope == gate.ScopeNone || slices.Contains(decision.Gates(), api.GateSecretOutbound) {
		return nil
	}
	var accesses []hitl.GrantedPathDelta
	var paths []string
	for _, target := range targets {
		if target == nil {
			continue
		}
		access, _, ok := sharedTempTargetAccess(action, *target)
		if !ok || slices.Contains(accesses, access) {
			continue
		}
		accesses = append(accesses, access)
		paths = append(paths, access.Path)
	}
	if len(accesses) == 0 {
		return nil
	}
	var authority []hitl.ApprovalAuthorityDelta
	var coverages []string
	var firstGrant hitl.ApprovalGrant
	for i, access := range accesses {
		part := sharedTempSingleChatOffer(action, access, hitl.TitleAllowForThisChat, access.Tree && !access.Write)
		if i == 0 {
			firstGrant = part.Grant
		}
		authority = append(authority, part.Authority...)
		coverages = append(coverages, part.Coverage)
	}
	sum := sha256.Sum256([]byte(strings.Join(append([]string{"shared_temp_multi", action.Scope.ChatSession()}, coverages...), "\x00")))
	id := "grant_" + hex.EncodeToString(sum[:8])
	coverage := strings.Join(coverages, "; ")
	offers := []hitl.ApprovalGrantOffer{{
		ID: id, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: hitl.TitleAllowForThisChat, Coverage: coverage,
		ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: hitl.ReaskWhenDifferentPath,
		Subject: gate.ReusePredicate, Grant: firstGrant, Authority: authority,
	}}
	if commonDir := commonSharedTempContainer(paths); commonDir != "" {
		offers = append(offers, hitl.SharedTempExchangeFolderOffer(action, commonDir, isSharedTempTopRoot(commonDir)))
	}
	return offers
}

func sharedTempSingleChatOffer(action hitl.ProposedAction, access hitl.GrantedPathDelta, title string, dirRead bool) hitl.ApprovalGrantOffer {
	chat := action.Scope.ChatSession()
	coverage := grantedPathCoverage(access)
	if dirRead && isSharedTempTopRoot(access.Path) {
		coverage += " — other applications' temporary files in " + access.Path + " are included"
	}
	reaskWhen := hitl.ReaskWhenDifferentPath
	if access.Tree {
		reaskWhen = hitl.ReaskWhenOutsideFolder
	}
	id := grantedPathGrantID(action, access, hitl.ApprovalGrantScopeChat, hitl.ApprovalRungChat)
	now := time.Now().UTC()
	accessCopy := access
	pathGrant := hitl.ApprovalGrant{
		ID: id, Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryPath), Pattern: access.Path},
		ChatSessionID: chat, ProjectID: action.Scope.ProjectID, ProjectDir: action.Scope.ProjectDir,
		Title: title, Coverage: coverage, GrantedAt: now,
		ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: reaskWhen,
		Source: "checkpoint", GrantedPath: &accessCopy,
	}
	authority := []hitl.ApprovalAuthorityDelta{}
	if !access.Write {
		readGrant := pathGrant
		readGrant.ID = id + "_r"
		readGrant.Predicate = hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: access.Path}
		authority = append(authority, hitl.ApprovalAuthorityDelta{
			Kind: hitl.AuthorityReadPathChat, Grant: &readGrant,
			ChatSessionID: chat, ReadPaths: []string{access.Path},
		})
	}
	grantCopy := pathGrant
	authority = append(authority,
		hitl.ApprovalAuthorityDelta{
			Kind: hitl.AuthorityGrantedPath, Grant: &grantCopy,
			ChatSessionID: chat, GrantedPath: &accessCopy,
		},
		hitl.ApprovalAuthorityDelta{
			Kind: hitl.AuthorityGenericGrant, Grant: &grantCopy,
		},
	)
	return hitl.ApprovalGrantOffer{
		ID: id, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: title, Coverage: coverage, ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: reaskWhen, Subject: gate.ReusePredicate, Grant: pathGrant, Authority: authority,
	}
}

func isSharedTempTopRoot(path string) bool {
	loc := confine.SharedTempLocation(path)
	return loc != "" && confine.PathEqual(fspath.CanonicalPath(path), loc)
}

func sharedTempContainerDir(path string) string {
	loc := confine.SharedTempLocation(path)
	if loc == "" {
		return ""
	}
	if confine.PathEqual(path, loc) {
		return loc
	}
	if _, exists, isDir := probeTargetFolder(path); exists && isDir {
		return path
	}
	dir := filepath.Dir(path)
	if !confine.PathAtOrUnder(dir, loc) {
		return loc
	}
	return dir
}

func commonSharedTempContainer(paths []string) string {
	var root string
	var dirs []string
	for _, p := range paths {
		loc := confine.SharedTempLocation(p)
		if loc == "" {
			return ""
		}
		if root == "" {
			root = loc
		} else if !confine.PathEqual(root, loc) {
			return ""
		}
		dirs = append(dirs, sharedTempContainerDir(p))
	}
	if len(dirs) == 0 {
		return ""
	}
	common := dirs[0]
	for _, d := range dirs[1:] {
		for !confine.PathAtOrUnder(d, common) {
			parent := filepath.Dir(common)
			if parent == common {
				return root
			}
			common = parent
		}
	}
	if !confine.PathAtOrUnder(common, root) {
		return root
	}
	return common
}
