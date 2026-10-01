package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
)

// HostWriteRoot prefers a worker's isolated branch.
func HostWriteRoot(tctx ToolContext) string {
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		return branch
	}
	return strings.TrimSpace(tctx.ActiveRootPath())
}

// ConfineRootsForAction shares write authority between policy and confinement.
func ConfineRootsForAction(tctx ToolContext) []string {
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		return []string{branch}
	}
	out := make([]string, 0, len(tctx.Roots)+1)
	if s := HostWriteRoot(tctx); s != "" {
		out = append(out, s)
	}
	for _, r := range tctx.Roots {
		if s := strings.TrimSpace(r.Path); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ConfineReadRootsForAction returns host-managed read allow-backs. A worker
// branch may sit below a denied primary ancestor, so its exact branch is added.
func ConfineReadRootsForAction(tctx ToolContext) []string {
	out := append([]string(nil), tctx.ReadRoots...)
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		out = append(out, branch)
	}
	return uniqueRoots(out)
}

// ValidateAttachedRootsForAction converts an attached-lane boundary refusal
// into a structured tool reject.
func ValidateAttachedRootsForAction(roots []string) *ToolReject {
	return writeRootRefusalReject(confine.ValidateAttachedWriteRoots(roots))
}

// ValidateGrantedRootsForAction converts a granted-lane boundary refusal into
// a structured tool reject.
func ValidateGrantedRootsForAction(roots []string) *ToolReject {
	return writeRootRefusalReject(confine.ValidateGrantedWriteRoots(roots))
}

// writeRootRefusalReject publishes refusals under the registered guidance code;
// the confine lane code rides along as the machine reason.
func writeRootRefusalReject(err *confine.WriteRootRefusalError) *ToolReject {
	if err == nil {
		return nil
	}
	code := isolation.CodeCapabilityRequestInvalid
	if err.Code == confine.WriteRootCodeControlPlane {
		code = isolation.CodeControlPlaneDenied
	}
	return &ToolReject{
		Code: code,
		Data: map[string]any{
			"path":   filepath.ToSlash(err.Path),
			"reason": err.Code,
		},
	}
}
