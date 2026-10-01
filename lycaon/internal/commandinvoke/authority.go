package commandinvoke

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/editorturn"
	"github.com/lycaon/lycaon/pkg/api"
)

// Denial is a policy refusal whose reason is host copy for the person.
type Denial struct{ Reason string }

func (d *Denial) Error() string { return d.Reason }

func deny(format string, args ...any) error {
	return &Denial{Reason: fmt.Sprintf(format, args...)}
}

// PolicyAuthority verifies contributed actions against machine state.
type PolicyAuthority struct {
	FindingNamesPath func(context.Context, string, string) (bool, error)
	DocumentRevision func(ctx context.Context, projectID, rootID, path string) (int64, bool, error)
}

// Authorize implements Authority.
func (a PolicyAuthority) Authorize(ctx context.Context, in Input) error {
	if in.Frame == nil || in.Command == nil {
		return fmt.Errorf("invocation carries no captured frame")
	}
	catalog := in.Frame.View.Catalog
	resolved, ok := in.Frame.View.Contributions.ResolveCommand(in.Command)
	if !ok {
		return deny("Command operation graph is not resolved")
	}
	in.Resolved = resolved
	provider := in.CommandID.Provider
	if !catalog.PackContributed(provider) {
		return deny("Provider %s is not contributing in the captured catalog", provider)
	}
	actionProvider := provider
	for _, operationID := range in.Resolved.Chain {
		if !catalog.PackContributed(operationID.Provider) {
			return deny("Operation provider %s is not contributing in the captured catalog", operationID.Provider)
		}
		actionProvider = operationID.Provider
	}
	if contribution.ProviderPolicyFor(in.Resolved.Action.Kind) == contribution.PolicyStockOnly &&
		!catalog.StockAuthority(actionProvider) {
		return deny("Action %s requires the stock trust root; %s does not hold it",
			in.Resolved.Action.Kind, actionProvider)
	}
	if in.Resolved.Action.Kind == contribution.ActionEditorAction {
		return a.authorizeEditorAction(ctx, in)
	}
	return nil
}

func (a PolicyAuthority) authorizeEditorAction(ctx context.Context, in Input) error {
	ref, err := contribution.ParseID(in.Resolved.Action.Ref)
	if err != nil {
		return fmt.Errorf("action ref: %w", err)
	}
	action, ok := in.Frame.View.Contributions.EditorAction(ref)
	if !ok {
		return deny("Editor action %s is not in the captured frame", ref)
	}
	boundary, ok := editorturn.PresetBoundaryFor(action.Execution.Preset)
	if !ok {
		return deny("Preset %s has no host implementation", action.Execution.Preset)
	}
	if boundary.RequiresTarget && in.Context.Path == "" {
		return deny("Preset %s requires a structured file target", action.Execution.Preset)
	}
	if boundary.RequiresFinding {
		if in.Context.FindingID == "" {
			return deny("Preset %s requires a recorded finding", action.Execution.Preset)
		}
		if a.FindingNamesPath == nil {
			return deny("No recorded finding names %s in this session", in.Context.Path)
		}
		named, err := a.FindingNamesPath(ctx, in.SessionID, in.Context.Path)
		if err != nil {
			return fmt.Errorf("verify finding target: %w", err)
		}
		if !named {
			return deny("No recorded finding names %s in this session", in.Context.Path)
		}
	}
	if err := requireTarget(action.Target, in.Context); err != nil {
		return err
	}
	return a.verifyDocumentRevision(ctx, boundary, in)
}

// verifyDocumentRevision binds writes to the tracked document version.
func (a PolicyAuthority) verifyDocumentRevision(
	ctx context.Context, boundary editorturn.PresetBoundary, in Input,
) error {
	if in.Context.DocumentRevision <= 0 {
		if boundary.Writes {
			return deny("A writing editor action requires the tracked document revision")
		}
		return nil
	}
	if a.DocumentRevision == nil {
		return fmt.Errorf("document revision cannot be verified: no document source is configured")
	}
	tracked, found, err := a.DocumentRevision(ctx, in.ProjectID, in.Context.RootID, in.Context.Path)
	if err != nil {
		return fmt.Errorf("verify document revision: %w", err)
	}
	if !found || tracked != int64(in.Context.DocumentRevision) {
		return deny("Document revision %d does not match the tracked document", in.Context.DocumentRevision)
	}
	return nil
}

// requireTarget checks the declared target shape.
func requireTarget(target contribution.EditorTarget, ctx api.CommandInvokeContext) error {
	if !target.Required {
		return nil
	}
	present := map[contribution.TargetKind]bool{
		contribution.TargetInstruction: strings.TrimSpace(ctx.Instruction) != "",
		contribution.TargetSymbol:      strings.TrimSpace(ctx.Symbol) != "",
		contribution.TargetFinding:     strings.TrimSpace(ctx.FindingID) != "",
		contribution.TargetFile:        strings.TrimSpace(ctx.Path) != "",
		contribution.TargetCaret:       ctx.StartLine > 0,
		contribution.TargetSelection:   ctx.StartLine > 0 && ctx.EndLine >= ctx.StartLine,
	}
	if !present[target.Kind] {
		return deny("The editor action requires a %s target", target.Kind)
	}
	return nil
}
