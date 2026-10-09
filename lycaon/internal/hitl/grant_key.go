package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// GrantKey returns an empty, unusable identity when the action cannot be encoded.
// Arguments stay out of the ledger because they may contain credentials.
func GrantKey(action ProposedAction) string {
	args, err := json.Marshal(grantIdentityArgs(action.Invocation.Tool, action.Invocation.Args))
	if err != nil {
		return ""
	}
	roots := append([]string(nil), action.Execution.Contained.Roots...)
	for i := range roots {
		roots[i] = strings.TrimSpace(roots[i])
	}
	sort.Strings(roots)
	hostResources := canonicalIDs(action.Resources.HostResources)
	families := canonicalIDs(action.Resources.HostResourceFamilies)
	packageIdentity := packageExecutionIdentity(action)
	boundary, err := json.Marshal(struct {
		FSJailed bool     `json:"fs_jailed"`
		Egress   string   `json:"egress"`
		Roots    []string `json:"roots"`
	}{
		FSJailed: action.Execution.Contained.FSJailed,
		Egress:   strings.TrimSpace(action.Execution.Contained.Egress),
		Roots:    roots,
	})
	if err != nil {
		return ""
	}
	identity := strings.TrimSpace(action.Invocation.Tool) + "\x00" +
		strings.TrimSpace(action.Scope.ProjectDir) + "\x00" + string(args) + "\x00" +
		strings.Join(hostResources, "\x1f") + "\x00" +
		strings.Join(families, "\x1f") + "\x00" + string(boundary) + "\x00" +
		packageIdentity
	if action.Execution.ProcessAccess != "" {
		raw, err := json.Marshal(action.Execution.ProcessTargets)
		if err != nil {
			return ""
		}
		identity += "\x00process:" + action.Execution.ProcessAccess + string(raw)
	}
	if action.Execution.ExecutionBoundaryDigest != "" {
		identity += "\x00execution:" + action.Execution.ExecutionBoundaryDigest
	}
	if action.Execution.Contained.ProcessControl {
		identity += "\x00process_control"
	}
	if action.Execution.Contained.HostExecution {
		identity += "\x00host_execution"
	}
	if len(action.Mutations.FileChanges) > 0 {
		changes, err := json.Marshal(action.Mutations.FileChanges)
		if err != nil {
			return ""
		}
		identity += "\x00" + string(changes)
	}
	if len(action.Mutations.AgentPolicy) > 0 {
		identity += "\x00" + strings.Join(canonicalIDs(AgentPolicyPaths(action.Mutations.AgentPolicy)), "\x1f")
	}
	digest := sha256.Sum256([]byte(identity))
	return "action_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func packageExecutionIdentity(action ProposedAction) string {
	if action.Execution.PackageExecution == nil {
		return ""
	}
	identities := make([]string, 0, len(action.Execution.PackageExecution.Packages))
	for _, pkg := range action.Execution.PackageExecution.Packages {
		identities = append(identities, pkg.ExactIdentity())
	}
	sort.Strings(identities)
	return strings.TrimSpace(action.Execution.PackageExecution.Manager) + "\x1e" +
		string(action.Execution.PackageExecution.Operation) + "\x1e" + strings.Join(identities, "\x1d")
}

// grantIdentityArgs normalizes grant identity without changing the invocation.
func grantIdentityArgs(tool string, args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	neutral := toolcontract.GrantIdentityNeutralArgs(tool)
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	for _, key := range neutral {
		delete(out, key)
	}
	if request, ok := out["capability_request"].(map[string]any); ok {
		out["capability_request"] = canonicalCapabilityRequest(request)
	}
	return out
}

// Declared destinations are a set in the egress boundary.
func canonicalCapabilityRequest(request map[string]any) map[string]any {
	out := make(map[string]any, len(request))
	for key, value := range request {
		out[key] = value
	}
	switch direct := out["direct_ip"].(type) {
	case []any:
		out["direct_ip"] = sortedStringList(direct)
	case map[string]any:
		narrowed := make(map[string]any, len(direct))
		for key, value := range direct {
			narrowed[key] = value
		}
		if declared, ok := narrowed["declared_destinations"].([]any); ok {
			narrowed["declared_destinations"] = sortedStringList(declared)
		}
		out["direct_ip"] = narrowed
	}
	return out
}

// sortedStringList orders a homogeneous string list and leaves any other shape
// alone, so a malformed request still reaches its own rejection unchanged.
func sortedStringList(values []any) []any {
	strs := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return values
		}
		strs = append(strs, s)
	}
	sort.Strings(strs)
	out := make([]any, 0, len(strs))
	for _, s := range strs {
		out = append(out, s)
	}
	return out
}

func canonicalIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
