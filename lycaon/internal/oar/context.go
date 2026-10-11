package oar

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ContentSegment is one host-attributed segment at a content anchor. These
// fields come from the structured occurrence envelope.
type ContentSegment struct {
	Content   string
	Role      string
	Origin    string
	Authority string
	TrustTier string
	Source    string
}

// GuardContext composes host fact domains for one anchor occurrence.
type GuardContext struct {
	Invocation         InvocationFacts
	Session            SessionFacts
	Progress           ProgressFacts
	Workflow           WorkflowFacts
	Workers            WorkersFacts
	Grounding          GroundingFacts
	Execution          ExecutionFacts
	Refusals           RefusalsFacts
	Source             SourceFacts
	Access             AccessFacts
	Content            ContentFacts
	Rejection          RejectionFacts
	MCP                MCPFacts
	Counters           CountersFacts
	lazy               providerState
	FeedbackData       map[string]any
	Anchor             string
	RejectData         map[string]map[string]any
	ObservationData    map[string]any
	ObservedRejectCode string
	Published          map[string]any
}

// ClearMCPObservation zeros all MCP structural facts and clears MCPFields.
func (gc *GuardContext) ClearMCPObservation() {
	if gc == nil {
		return
	}
	gc.MCP = MCPFacts{}
}

// FactProvider computes one named fact into gc. Observation only.
type FactProvider func(gc *GuardContext) error

// NewGuardContext returns an empty fact set.
func NewGuardContext() *GuardContext {
	return &GuardContext{
		Invocation: InvocationFacts{ToolArgs: map[string]any{}},
		Access:     AccessFacts{PathOutsideScopeByTool: map[string]bool{}},
		Source:     SourceFacts{SourceIncludes: map[string]bool{}},
		RejectData: map[string]map[string]any{},
		lazy: providerState{
			providers:      map[string]FactProvider{},
			computed:       map[string]bool{},
			providerErrors: map[string]error{},
		},
	}
}

// RegisterProvider registers a lazy fact provider by catalogue name.
func (gc *GuardContext) RegisterProvider(name string, p FactProvider) {
	if gc == nil || name == "" || p == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	if gc.lazy.providers == nil {
		gc.lazy.providers = map[string]FactProvider{}
	}
	gc.lazy.providers[name] = p
}

// Ensure runs the named provider once if registered.
func (gc *GuardContext) Ensure(name string) error {
	if gc == nil || name == "" {
		return nil
	}
	gc.lazy.mu.Lock()
	if gc.lazy.computed[name] {
		err := gc.lazy.providerErrors[name]
		gc.lazy.mu.Unlock()
		return err
	}
	if pending := gc.lazy.providerRunning[name]; pending != nil {
		gc.lazy.mu.Unlock()
		<-pending
		gc.lazy.mu.Lock()
		err := gc.lazy.providerErrors[name]
		gc.lazy.mu.Unlock()
		return err
	}
	if gc.lazy.providerRunning == nil {
		gc.lazy.providerRunning = map[string]chan struct{}{}
	}
	pending := make(chan struct{})
	gc.lazy.providerRunning[name] = pending
	provider := gc.lazy.providers[name]
	gc.lazy.mu.Unlock()
	var err error
	if provider != nil {
		err = provider(gc)
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	if gc.lazy.computed == nil {
		gc.lazy.computed = map[string]bool{}
	}
	if gc.lazy.providerErrors == nil {
		gc.lazy.providerErrors = map[string]error{}
	}
	gc.lazy.computed[name] = true
	gc.lazy.providerErrors[name] = err
	delete(gc.lazy.providerRunning, name)
	close(pending)
	return err
}

// SetCodeRejectResponses records the bounded rejected-response count as the
// code_reject_responses fact.
func (gc *GuardContext) SetCodeRejectResponses(n int64) {
	if gc == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	gc.Counters.CodeRejectResponses = n
}

// SetFruitlessSearchRun records the host-observed consecutive-fruitless-search
// run as the fruitless_search_run fact.
func (gc *GuardContext) SetFruitlessSearchRun(n int64) {
	if gc == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	gc.Counters.FruitlessSearchRun = n
}

// SetRepeatCount records an authoritative host repeat count.
func (gc *GuardContext) SetRepeatCount(n int64) {
	if gc == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	gc.Counters.RepeatCount = n
}

// SetDeferredUnactivated records how many of this surface's deferred tools the
// session has not yet activated.
func (gc *GuardContext) SetDeferredUnactivated(n int64) {
	if gc == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	gc.Counters.DeferredUnactivated = n
}

// SetContentSegments records the structured content occurrence and refreshes
// the aligned content-provenance profile plus the detector-facing text buffer.
func (gc *GuardContext) SetContentSegments(segments []ContentSegment) {
	if gc == nil {
		return
	}
	gc.lazy.mu.Lock()
	defer gc.lazy.mu.Unlock()
	gc.Content.ContentRoles = make([]string, 0, len(segments))
	gc.Content.ContentOrigins = make([]string, 0, len(segments))
	gc.Content.ContentAuthorities = make([]string, 0, len(segments))
	gc.Content.ContentTrustTiers = make([]string, 0, len(segments))
	gc.Content.ContentSources = make([]string, 0, len(segments))
	contents := make([]string, 0, len(segments))
	gc.Content.ContentContainsUntrusted = false
	for _, segment := range segments {
		contents = append(contents, segment.Content)
		gc.Content.ContentRoles = append(gc.Content.ContentRoles, provenanceValue(segment.Role))
		gc.Content.ContentOrigins = append(gc.Content.ContentOrigins, provenanceValue(segment.Origin))
		gc.Content.ContentAuthorities = append(gc.Content.ContentAuthorities, provenanceValue(segment.Authority))
		trust := provenanceValue(segment.TrustTier)
		gc.Content.ContentTrustTiers = append(gc.Content.ContentTrustTiers, trust)
		gc.Content.ContentSources = append(gc.Content.ContentSources, strings.TrimSpace(segment.Source))
		if trust == "untrusted" {
			gc.Content.ContentContainsUntrusted = true
		}
	}
	gc.Content.ContentSegmentCount = int64(len(segments))
	gc.Content.Content = strings.Join(contents, "\n")
	gc.Content.ContentSet = true
	gc.Content.ContentLength = int64(len([]rune(gc.Content.Content)))
}

func provenanceValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

// DeriveToolClassFacts records tool identity and its declared contract.
func (gc *GuardContext) DeriveToolClassFacts() {
	if gc == nil {
		return
	}
	t := gc.Invocation.Tool
	gc.Invocation.ToolIsState = hasPrefix(t, "state_")
	gc.Invocation.ToolIsDelegation = hasPrefix(t, "delegate_")
	gc.Invocation.ToolIsHandoff = hasPrefix(t, "handoff_")
	gc.Invocation.ToolIsTask = t == "task"
	gc.Invocation.PackRunnerTask = t == "task" // observation: task tool used; agent-type filter is separate
	if contract, ok := toolcontract.Lookup(t); ok {
		gc.Invocation.ToolPayloadChunkable = contract.ChunkablePayload
	}
	gc.Session.PostureUnresolved = gc.Session.SessionPosture == "" || gc.Session.SessionPosture == "unresolved"
	if gc.Session.PermissionProfile == "" {
		gc.Session.PermissionProfile = gc.Session.Profile
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}
