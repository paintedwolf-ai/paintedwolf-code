package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// HostWriteRoot prefers a worker's isolated branch.
func HostWriteRoot(tctx ToolContext) string {
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		return branch
	}
	return strings.TrimSpace(tctx.ActiveRootPath())
}

// ConfineRootsForAction shares write authority between policy and confinement.
func ConfineRootsForAction(tctx ToolContext) []string {
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		return workerBranchRoots(branch, tctx.Source.Roots)
	}
	out := make([]string, 0, len(tctx.Source.Roots)+1)
	if s := HostWriteRoot(tctx); s != "" {
		out = append(out, s)
	}
	for _, r := range tctx.Source.Roots {
		if s := strings.TrimSpace(r.Path); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// workerBranchRoots lists the project roots inside a branch. One root fills the
// branch; several each own a directory, and every one carries its own policy.
func workerBranchRoots(branch string, sources []projectroot.RootRef) []string {
	out := []string{branch}
	if len(sources) <= 1 {
		return out
	}
	for _, source := range sources {
		if dir, err := projectroot.BranchDirForID(source.ID); err == nil {
			out = append(out, filepath.Join(branch, dir))
		}
	}
	return out
}

// ConfineReadRootsForAction returns host-managed read allow-backs. A worker
// branch may sit below a denied primary ancestor, so its exact branch is added.
func ConfineReadRootsForAction(tctx ToolContext) []string {
	out := append([]string(nil), tctx.Files.ReadRoots...)
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		out = append(out, branch)
	}
	return UniqueRoots(out)
}

// ValidateAttachedRootsForAction converts an attached-lane boundary refusal
// into a structured tool reject.
func ValidateAttachedRootsForAction(roots []string) *toolrejection.ToolReject {
	return writeRootRefusalReject(confine.ValidateAttachedWriteRoots(roots))
}

// ValidateGrantedRootsForAction converts a granted-lane boundary refusal into
// a structured tool reject.
func ValidateGrantedRootsForAction(roots []string) *toolrejection.ToolReject {
	return writeRootRefusalReject(confine.ValidateGrantedWriteRoots(roots))
}

// writeRootRefusalReject publishes refusals under the registered guidance code;
// the confine lane code rides along as the machine reason.
func writeRootRefusalReject(err *confine.WriteRootRefusalError) *toolrejection.ToolReject {
	if err == nil {
		return nil
	}
	code := isolation.CodeCapabilityRequestInvalid
	if err.Code == confine.WriteRootCodeControlPlane || err.Code == confine.WriteRootCodeContainsState {
		code = isolation.CodeControlPlaneDenied
	}
	return &toolrejection.ToolReject{
		Code: code,
		Data: map[string]any{
			"path":   filepath.ToSlash(err.Path),
			"reason": err.Code,
		},
	}
}
