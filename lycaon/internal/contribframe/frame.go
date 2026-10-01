// Package contribframe captures immutable contribution runtime inputs.
package contribframe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/pkg/api"
)

// Frame is one captured generation pair plus derived immutable indexes.
type Frame struct {
	// View retains the captured catalog generation.
	View *catalogview.View
	// MCP retains the captured resource generation.
	MCP *mcp.ResourceGeneration
	// Revision identifies both captured generations.
	Revision string

	requirements map[contribution.ID]RequirementStatus
	subgraphs    map[contribution.ID]string
}

// RequirementStatus joins a requirement to captured provider state.
type RequirementStatus struct {
	ID    contribution.ID
	Ready bool
	// Reason is a closed code when not ready: provider_missing,
	// provider_disabled, provider_not_ready, or tool_missing.
	Reason string
	// MissingTools lists absent required tools.
	MissingTools []string
}

// frameRevisionDomain versions frame interpretation and encoding.
const frameRevisionDomain = "painted-wolf/contribution-frame/1"

// subgraphDomain versions the per-command compiled subgraph identity.
const subgraphDomain = "painted-wolf/contribution-subgraph/1"

// ErrSourceUnavailable reports that no compiled catalog view or MCP generation
// was available to capture.
var ErrSourceUnavailable = errors.New("contribframe: contribution source unavailable")

// Build derives an immutable frame from captured generations.
func Build(view *catalogview.View, gen *mcp.ResourceGeneration) (*Frame, error) {
	if view == nil || view.Contributions == nil {
		return nil, fmt.Errorf("%w: no catalog view", ErrSourceUnavailable)
	}
	if gen == nil {
		return nil, fmt.Errorf("%w: no mcp resource generation", ErrSourceUnavailable)
	}
	f := &Frame{
		View:         view,
		MCP:          gen,
		requirements: map[contribution.ID]RequirementStatus{},
		subgraphs:    map[contribution.ID]string{},
	}
	set := view.Contributions
	for _, req := range set.MCPRequirements() {
		id, err := contribution.ParseID(req.ID)
		if err != nil {
			return nil, fmt.Errorf("contribframe: requirement id: %w", err)
		}
		f.requirements[id] = joinRequirement(id, req, gen)
	}
	for _, command := range set.Commands() {
		id, err := contribution.ParseID(command.ID)
		if err != nil {
			return nil, fmt.Errorf("contribframe: command id: %w", err)
		}
		f.subgraphs[id] = f.subgraphIdentity(id, command)
	}

	h := sha256.New()
	field := func(label, body string) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f%s", label, len(body), body)
	}
	field("domain", frameRevisionDomain)
	field("catalog_revision", view.Catalog.Revision)
	field("mcp_revision", gen.Revision)
	f.Revision = hex.EncodeToString(h.Sum(nil))
	return f, nil
}

func joinRequirement(id contribution.ID, req *contribution.MCPRequirement, gen *mcp.ResourceGeneration) RequirementStatus {
	status := RequirementStatus{ID: id}
	provider, ok := gen.Provider(req.ProviderID)
	switch {
	case !ok:
		status.Reason = "provider_missing"
		return status
	case !provider.Enabled:
		status.Reason = "provider_disabled"
		return status
	case provider.Status != api.McpStatusReady:
		status.Reason = "provider_not_ready"
		return status
	}
	for _, tool := range req.RequiredTools {
		if _, ok := gen.Tool(req.ProviderID, tool); !ok {
			status.MissingTools = append(status.MissingTools, tool)
		}
	}
	if len(status.MissingTools) > 0 {
		status.Reason = "tool_missing"
		return status
	}
	status.Ready = true
	return status
}

// subgraphIdentity hashes one command's execution dependencies.
func (f *Frame) subgraphIdentity(id contribution.ID, command *contribution.Command) string {
	set := f.View.Contributions
	h := sha256.New()
	field := func(label string, body []byte) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f", label, len(body))
		_, _ = h.Write(body)
	}
	str := func(label, body string) { field(label, []byte(body)) }
	str("domain", subgraphDomain)
	if unit, ok := set.Unit(id); ok {
		field("command", unit.Body)
	}
	resolved, ok := set.ResolveCommand(command)
	if !ok {
		return hex.EncodeToString(h.Sum(nil))
	}
	for _, operationID := range resolved.Chain {
		if unit, exists := set.Unit(operationID); exists {
			field("operation", unit.Body)
		}
	}
	switch resolved.Action.Kind {
	case contribution.ActionEditorAction:
		if ref, err := contribution.ParseID(resolved.Action.Ref); err == nil {
			if unit, ok := set.Unit(ref); ok {
				field("editor_action", unit.Body)
			}
			if action, ok := set.EditorAction(ref); ok {
				str("prompt_ref", action.Execution.PromptRef)
				field("prompt", f.PromptBody(action.Execution.PromptRef))
			}
		}
	case contribution.ActionWorkflowStart:
		workflowUnit := "workflows/" + strings.TrimSpace(resolved.Action.Workflow)
		str("workflow", workflowUnit)
		field("workflow_unit", f.PromptBody(workflowUnit))
	case contribution.ActionMCPTool:
		str("tool", resolved.Action.Tool)
		if ref, err := contribution.ParseID(resolved.Action.Requirement); err == nil {
			if unit, ok := set.Unit(ref); ok {
				field("requirement", unit.Body)
			}
			str("requirement_ready", fmt.Sprintf("%t", f.requirements[ref].Ready))
		}
	case contribution.ActionComposerPrefill, contribution.ActionNavigate, contribution.ActionExternalLink,
		contribution.ActionNativeUI, contribution.ActionOperation:
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PromptBody returns the winning bytes of a loaded prompt unit, or nil.
func (f *Frame) PromptBody(unitID string) []byte {
	if f.View.Catalog == nil {
		return nil
	}
	if unit, ok := f.View.Catalog.Loaded[unitID]; ok {
		return unit.Content
	}
	return nil
}

// SubgraphIdentity returns the compiled subgraph identity for one command.
func (f *Frame) SubgraphIdentity(id contribution.ID) (string, bool) {
	identity, ok := f.subgraphs[id]
	return identity, ok
}

// Command resolves one command declaration from the captured frame.
func (f *Frame) Command(id contribution.ID) (*contribution.Command, bool) {
	return f.View.Contributions.Command(id)
}

// Requirement returns the joined status for one MCP requirement.
func (f *Frame) Requirement(id contribution.ID) (RequirementStatus, bool) {
	status, ok := f.requirements[id]
	return status, ok
}

// RequirementStatuses returns every joined requirement sorted by id.
func (f *Frame) RequirementStatuses() []RequirementStatus {
	out := make([]RequirementStatus, 0, len(f.requirements))
	for _, status := range f.requirements {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

// RequirementReady reads captured provider readiness.
func (f *Frame) RequirementReady(operand string) bool {
	id, err := contribution.ParseID(operand)
	if err != nil {
		return false
	}
	return f.requirements[id].Ready
}

// ConfigurationOn reads captured effective configuration.
func (f *Frame) ConfigurationOn(operand string) bool {
	if f == nil || f.View == nil {
		return false
	}
	return f.View.Contributions.ConfigurationOn(operand)
}
