package toolexecution

import (
	"context"
	"fmt"
	"math"
	"sync"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramModelInvocation is the actual submitted model call, before command
// parsing or stage normalization. The host supplies its call ID separately.
type ProgramModelInvocation struct {
	ToolName  string
	Arguments map[string]any
}

// ProgramModelObservation borrows immutable submitted JSON from its frame.
// Consumers must finish using it before Close, or admit their own retained copy.
type ProgramModelObservation struct {
	ToolName           string
	ToolCallID         string
	CanonicalArguments string
}

type programFrameLifetime struct {
	mu        sync.Mutex
	closed    bool
	model     ProgramModelObservation
	arguments *nativeArguments
	releases  []func()
}

func captureProgramModel(ctx context.Context, parent toolcontract.OperationInvocation, tc tools.ToolContext, model ProgramModelInvocation, admission ProgramStateAdmission) (*programFrameLifetime, error) {
	if ctx == nil || admission == nil || model.ToolName != "command" || model.Arguments == nil {
		return nil, fmt.Errorf("program frame requires actual command invocation and retained state admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reserve the root map and scalar attribution before the addressed map is
	// allocated. Strings remain immutable host values; this accounts retention.
	bound := int64(4096)
	add := func(value string) bool {
		if int64(len(value)) > (math.MaxInt64-bound)/8 {
			return false
		}
		bound += int64(len(value)) * 8
		return true
	}
	for _, value := range []string{parent.ActorID, parent.SessionID, parent.SourceUnitID, parent.CatalogDigest, parent.Identity.ProgramID, parent.Identity.NodeID, parent.Identity.ExecutionID, parent.Identity.ParentExecutionID, parent.Scope.ProjectID, parent.Scope.RootID, parent.Scope.BranchID, parent.Scope.Directory, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID, tc.Identity.MessageID, tc.Identity.Agent, tc.Identity.WorkerJobID, tc.Identity.ParentSessionID, tc.Identity.RootSessionID, tc.Identity.HandoffSessionID, tc.Identity.HandoffAgentID, tc.Source.WorkerBranchRoot, tc.Turn.TurnSurfaceID} {
		if !add(value) {
			return nil, fmt.Errorf("program frame scalar bound overflow")
		}
	}
	for _, root := range tc.Source.Roots {
		if bound > math.MaxInt64-512 {
			return nil, fmt.Errorf("program root bound overflow")
		}
		bound += 512
		if !add(root.ID) || !add(root.Path) {
			return nil, fmt.Errorf("program root bound overflow")
		}
		branch, err := tc.SourceBranch(root.ID)
		if err != nil {
			return nil, err
		}
		if !add(branch.String()) {
			return nil, fmt.Errorf("program branch bound overflow")
		}
	}
	release, err := admission.ReserveProgramState(ctx, bound)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("program frame admission returned no lease")
	}
	lifetime := &programFrameLifetime{releases: []func(){release}}
	snapshot, err := prepareNativeArguments(ctx, admission, model.Arguments)
	if err != nil {
		lifetime.close()
		return nil, err
	}
	lifetime.arguments = snapshot
	if !modelJSONUTF8(snapshot.values) {
		lifetime.close()
		return nil, fmt.Errorf("submitted model arguments require valid JSON text strings")
	}
	canonical, err := authzcontext.CanonicalJSON(snapshot.values)
	if err != nil {
		lifetime.close()
		return nil, err
	}
	lifetime.model = ProgramModelObservation{ToolName: model.ToolName, ToolCallID: tc.Identity.ToolCallID, CanonicalArguments: string(canonical)}
	snapshot.values = nil
	return lifetime, nil
}

func (f ProgramFrame) alive() error {
	if f.lifetime == nil {
		return fmt.Errorf("uncaptured program frame")
	}
	f.lifetime.mu.Lock()
	defer f.lifetime.mu.Unlock()
	if f.lifetime.closed {
		return fmt.Errorf("program frame is closed")
	}
	return nil
}

// ModelInvocation returns the actual initiating call without reconstructing it
// from native operation arguments, display text, or parsed stage spelling.
func (f ProgramFrame) ModelInvocation(ctx context.Context) (ProgramModelObservation, error) {
	if ctx == nil {
		return ProgramModelObservation{}, fmt.Errorf("model observation requires context")
	}
	if err := ctx.Err(); err != nil {
		return ProgramModelObservation{}, err
	}
	if f.lifetime == nil {
		return ProgramModelObservation{}, fmt.Errorf("uncaptured program frame")
	}
	f.lifetime.mu.Lock()
	defer f.lifetime.mu.Unlock()
	if f.lifetime.closed {
		return ProgramModelObservation{}, fmt.Errorf("program frame is closed")
	}
	return f.lifetime.model, nil
}

// Close invalidates every copy of the frame. The command entry closes it only
// after all child admission, held review, owner calls and projections finish.
func (f ProgramFrame) Close() {
	if f.lifetime != nil {
		f.lifetime.close()
	}
}

func (l *programFrameLifetime) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.model = ProgramModelObservation{}
	if l.arguments != nil {
		l.arguments.close()
		l.arguments = nil
	}
	for i := len(l.releases) - 1; i >= 0; i-- {
		l.releases[i]()
	}
	l.releases = nil
}

// The model protocol is JSON text. Native operation operands use the separate
// lossless review representation; they do not pass through this outer snapshot.
func modelJSONUTF8(value any) bool {
	switch v := value.(type) {
	case string:
		return utf8.ValidString(v)
	case map[string]any:
		for key, item := range v {
			if !utf8.ValidString(key) || !modelJSONUTF8(item) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !modelJSONUTF8(item) {
				return false
			}
		}
	case []string:
		for _, item := range v {
			if !utf8.ValidString(item) {
				return false
			}
		}
	}
	return true
}
