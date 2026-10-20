package toolexecution

import (
	"context"
	"fmt"
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
		catalog, err := surfacecatalog.Load()
		if err != nil {
			return ProgramSurface{}, err
		}
		row, err := catalog.Surface(filter.SurfaceID)
		if err != nil {
			return ProgramSurface{}, err
		}
		restriction, err := toolcontract.NewCommandEffects(row.CommandEffects...)
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

// ValidateBound checks a binder-selected exact catalog mode. The adapter must
// bind actual operands, embedded utility programs and children before calling;
// untaken branches never call admission. Owners still check reached effects.
func (s ProgramSurface) ValidateBound(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, command, mode string, child *toolcontract.CommandEffects) error {
	if err := s.selection.known.Validate(definition, invocation); err != nil {
		return err
	}
	facts := definition.Facts()
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
