package toolexecution

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolreach"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramSurface pins exposure and effect restrictions for one program. It
// carries no filesystem, network or once-use effect permit.
type ProgramSurface struct {
	selection NativeOperationSelection
	allowed   toolcontract.CommandEffects
	profile   string
}

// AllowsEffects reports the frozen profile and surface restriction only.
// Reached operations still require full native admission.
func (s ProgramSurface) AllowsEffects(effects toolcontract.CommandEffects) bool {
	return s.allowed.Allows(effects)
}

// ValidateOperation admits already compiled immutable effect metadata. It is
// also the static-unit check; callers run it for every known prohibited effect
// before starting any program operation. It invokes no authority or owner.
func (s ProgramSurface) ValidateOperation(bound toolcontract.BoundOperation) error {
	definition, invocation := bound.Definition(), bound.Invocation()
	if err := s.selection.known.Validate(definition, invocation); err != nil {
		return err
	}
	facts := definition.ReadOnlyFacts()
	if s.selection.identityDenied[facts.Identity] || bound.Publication() == toolcontract.NoPublication && s.selection.denied[facts.Identity][bound.Command()] {
		return s.profileReject("COMMAND_OPERATION_NOT_ALLOWED", facts, invocation)
	}
	if !s.allowed.Allows(bound.Effects()) {
		return s.profileReject("COMMAND_EFFECT_NOT_ALLOWED", facts, invocation)
	}
	return nil
}

// FreezeProgramSurface uses the effective profile boundary and explicit native
// definitions. Existing profile/path grants remain separate executing checks.
func (e *Metadata) FreezeProgramSurface(ctx context.Context, filter platform.ToolFilter, active []toolcontract.OperationDefinition) (ProgramSurface, error) {
	if e == nil {
		return ProgramSurface{}, fmt.Errorf("command metadata required")
	}
	source, ok := e.policy.(interface {
		FreezeCommandProfile(context.Context, string) (context.Context, toolcontract.CommandEffects, error)
	})
	if !ok {
		return ProgramSurface{}, fmt.Errorf("command profile effect boundary required")
	}
	frozen, allowed, err := source.FreezeCommandProfile(ctx, filter.ProfileID)
	if err != nil {
		return ProgramSurface{}, err
	}
	if filter.ProfileID == toolcontract.CoordinatorProfileID && filter.SurfaceID == "" {
		return ProgramSurface{}, fmt.Errorf("coordinator program requires captured surface")
	}
	if filter.SurfaceID != "" {
		restriction, err := surfaceCommandEffects(filter.SurfaceID)
		if err != nil {
			return ProgramSurface{}, err
		}
		allowed &= restriction
	}
	selection, err := e.SelectNativeOperations(frozen, filter, active)
	if err != nil {
		return ProgramSurface{}, err
	}
	return ProgramSurface{selection: selection, allowed: allowed, profile: filter.ProfileID}, nil
}

// ProgramReach freezes which native programs a profile and surface can run: the
// catalog entry's effects fit the restriction and no profile deny names it.
func (e *Metadata) ProgramReach(ctx context.Context, filter platform.ToolFilter) (toolreach.Programs, error) {
	if e == nil {
		return nil, fmt.Errorf("command metadata required")
	}
	policy, ok := e.policy.(interface {
		FreezeCommandProfile(context.Context, string) (context.Context, toolcontract.CommandEffects, error)
		CommandOperationAllowed(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return nil, fmt.Errorf("command profile effect boundary required")
	}
	frozen, allowed, err := policy.FreezeCommandProfile(ctx, filter.ProfileID)
	if err != nil {
		return nil, err
	}
	if filter.SurfaceID != "" {
		restriction, err := surfaceCommandEffects(filter.SurfaceID)
		if err != nil {
			return nil, err
		}
		allowed &= restriction
	}
	surfaceTools, err := surfaceCommandFormTools(filter.SurfaceID)
	if err != nil {
		return nil, err
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	reachable := map[string]bool{}
	for _, spec := range catalog.Commands {
		permitted, err := policy.CommandOperationAllowed(frozen, filter.ProfileID, spec.Name, spec.Name)
		if err != nil {
			return nil, err
		}
		if !surfaceAllowsCommandForm(surfaceTools, spec.Name) {
			permitted = false
		}
		reachable[spec.Name] = permitted && anyModeAllowed(spec, allowed)
	}
	return func(name string) bool { return reachable[name] }, nil
}

func surfaceCommandFormTools(surfaceID string) (map[string]bool, error) {
	if strings.TrimSpace(surfaceID) == "" {
		return nil, nil
	}
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	row, err := catalog.Surface(surfaceID)
	if err != nil {
		return nil, err
	}
	tools := make(map[string]bool, len(row.Floor)+len(row.Loadable))
	for _, name := range row.FloorTools() {
		tools[name] = true
	}
	for _, name := range row.LoadableTools() {
		tools[name] = true
	}
	return tools, nil
}

func surfaceAllowsCommandForm(surfaceTools map[string]bool, command string) bool {
	if surfaceTools == nil {
		return true
	}
	identities, err := toolcontract.CommandPolicyIdentities(command)
	if err != nil {
		return false
	}
	for _, candidate := range identities {
		if toolcontract.CommandFormTool(candidate) && !surfaceTools[candidate] {
			return false
		}
	}
	return true
}

func anyModeAllowed(spec toolcontract.CommandSpecification, allowed toolcontract.CommandEffects) bool {
	for _, requirement := range spec.Effects {
		// A wrapper's effects depend on its child, which is checked when it runs.
		effects, err := requirement.CommandEffects(&allowed)
		if err == nil && allowed.Allows(effects) {
			return true
		}
	}
	return false
}

func surfaceCommandEffects(surfaceID string) (toolcontract.CommandEffects, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return 0, err
	}
	row, err := catalog.Surface(surfaceID)
	if err != nil {
		return 0, err
	}
	return toolcontract.NewCommandEffects(row.CommandEffects...)
}

// ValidateBound checks a binder-selected exact catalog mode. The adapter must
// bind actual operands, embedded utility programs and children before calling;
// untaken branches never call admission. Owners still check reached effects.
func (s ProgramSurface) ValidateBound(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, command, mode string, child *toolcontract.CommandEffects) error {
	if err := s.selection.known.Validate(definition, invocation); err != nil {
		return err
	}
	facts := definition.ReadOnlyFacts()
	if s.selection.denied[facts.Identity][command] {
		return s.profileReject("COMMAND_OPERATION_NOT_ALLOWED", facts, invocation)
	}
	bound, err := toolcontract.BindOperation(definition, invocation, command, mode, child)
	if err != nil {
		rejected := s.profileReject("COMMAND_NATIVE_FORM_UNSUPPORTED", facts, invocation)
		rejected.FailureClass = api.FailureClassHostRejection
		return rejected
	}
	if s.allowed.Allows(bound.Effects()) {
		return nil
	}
	return s.profileReject("COMMAND_EFFECT_NOT_ALLOWED", facts, invocation)
}

func (s ProgramSurface) profileReject(code string, facts toolcontract.OperationFacts, invocation toolcontract.OperationInvocation) *toolrejection.ToolReject {
	var forms []string
	for _, command := range facts.Commands {
		if s.selection.denied[facts.Identity][command.Name] {
			continue
		}
		var modes []string
		for _, mode := range command.Effects {
			effects, err := mode.CommandEffects(nil)
			if err == nil && s.allowed.Allows(effects) {
				modes = append(modes, mode.Mode)
			}
		}
		if len(modes) == len(command.Effects) && len(modes) > 0 {
			forms = append(forms, command.Argv)
		} else if len(modes) > 0 {
			forms = append(forms, command.Name+" ["+strings.Join(modes, ", ")+"]")
		}
	}
	return &toolrejection.ToolReject{Code: code, FailureClass: api.FailureClassPolicyRejection, OwnerRef: facts.Contract.Owner, Data: map[string]any{
		"tool": facts.Identity, "permission_profile": s.profile, "expected_form": strings.Join(forms, "\n"),
		"source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte,
		"source_line": invocation.Span.Line, "source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID,
	}}
}
