package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
)

// ToolContext copies share one permit, consumed at the launch boundary.
type executionPermit struct {
	mu                            sync.Mutex
	session, call                 string
	boundary                      string
	arguments                     string
	processControl, hostExecution bool
	consumed                      bool
}

func finalizeExecutionCapability(tc ToolContext, req confine.Request) *toolrejection.ToolReject {
	if !req.HostExecution && !req.ProcessControl {
		return nil
	}
	permit := tc.executionPermit
	if permit == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.arguments != executionArgumentsDigest(tc.CanonicalArgs) || permit.boundary != ExecutionBoundaryDigest(req) || permit.consumed || permit.session != tc.SessionID || permit.call != tc.ToolCallID || permit.hostExecution != req.HostExecution || permit.processControl != req.ProcessControl {
		return &toolrejection.ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.consumed = true
	return nil
}

func ExecutionBoundaryDigest(req confine.Request) string {
	// Set order and duplicates do not change the reviewed boundary.
	req.Roots = executionBoundarySet(req.Roots)
	req.GrantedWriteRoots = executionBoundarySet(req.GrantedWriteRoots)
	req.ReadRoots = executionBoundarySet(req.ReadRoots)
	req.ReadDenyPaths = executionBoundarySet(req.ReadDenyPaths)
	req.SocketGrants = executionBoundarySet(req.SocketGrants)
	req.ProtectedWriteGrants = executionBoundarySet(req.ProtectedWriteGrants)
	req.PolicyWriteGrants = executionBoundarySet(req.PolicyWriteGrants)
	req.ProtectedReadGrants = executionBoundarySet(req.ProtectedReadGrants)
	req.DirectIPDeclared = executionBoundarySet(req.DirectIPDeclared)
	req.LocalListenPorts = executionBoundarySet(req.LocalListenPorts)
	req.LoopbackConnectPorts = executionBoundarySet(req.LoopbackConnectPorts)
	raw, err := json.Marshal(req)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func executionArgumentsDigest(args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Request set elements are strings, ports, or concrete path-grant records.
func executionBoundarySet[T comparable](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	out := slices.Clone(values)
	slices.SortFunc(out, func(a, b T) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)
		return bytes.Compare(left, right)
	})
	return slices.Compact(out)
}

// StampExecutionApproval binds reviewed authority to this call and launch boundary.
func (tc *ToolContext) StampExecutionApproval(args map[string]any, reviewed hitl.ProposedAction) {
	tc.executionPermit = &executionPermit{session: tc.SessionID, call: tc.ToolCallID, processControl: tc.ProcessControl, hostExecution: tc.HostExecution, boundary: reviewed.ExecutionBoundaryDigest, arguments: executionArgumentsDigest(args)}
}

// HasExecutionApproval reports whether this invocation reached capability review.
func (tc ToolContext) HasExecutionApproval() bool { return tc.executionPermit != nil }
