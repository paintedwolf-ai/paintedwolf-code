package toolexecution

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramSurface pins exposure and effect restrictions for one program. It
// carries no filesystem, network or once-use effect permit.
type ProgramSurface struct {
	offered commandprogram.OfferedOperations
	allowed toolcontract.CommandEffects
	profile string
}

// FreezeProgramSurface uses the effective profile boundary and registered
// definitions. Existing profile/path grants remain separate executing checks.
func (e *Metadata) FreezeProgramSurface(ctx context.Context, filter platform.ToolFilter, active []string) (ProgramSurface, error) {
	if e == nil {
		return ProgramSurface{}, fmt.Errorf("command metadata required")
	}
	source, ok := e.policy.(interface {
		CommandEffects(context.Context, string) (toolcontract.CommandEffects, error)
	})
	if !ok {
		return ProgramSurface{}, fmt.Errorf("command profile effect boundary required")
	}
	allowed, err := source.CommandEffects(ctx, filter.ProfileID)
	if err != nil {
		return ProgramSurface{}, err
	}
	offered, _, err := e.OfferProgramOperations(ctx, filter, active)
	if err != nil {
		return ProgramSurface{}, err
	}
	return ProgramSurface{offered: offered, allowed: allowed, profile: filter.ProfileID}, nil
}

// ValidateBound checks a binder-selected exact catalog mode. The adapter must
// bind actual operands, embedded utility programs and children before calling;
// untaken branches never call admission. Owners still check reached effects.
func (s ProgramSurface) ValidateBound(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, command, mode string, child *toolcontract.CommandEffects) error {
	if err := s.offered.Validate(definition, invocation); err != nil {
		return err
	}
	facts := definition.Facts()
	for _, spec := range facts.Commands {
		if spec.Name != command {
			continue
		}
		for _, requirements := range spec.Effects {
			if requirements.Mode != mode {
				continue
			}
			required, err := requirements.CommandEffects(child)
			if err != nil {
				return err
			}
			if s.allowed.Allows(required) {
				return nil
			}
			classes := s.allowed.Classes()
			names := make([]string, len(classes))
			for i, class := range classes {
				names[i] = string(class)
			}
			return &toolrejection.ToolReject{Code: "COMMAND_EFFECT_NOT_ALLOWED", FailureClass: api.FailureClassPolicyRejection, OwnerRef: facts.Contract.Owner, Data: map[string]any{
				"tool": facts.Identity, "permission_profile": s.profile, "expected_form": strings.Join(names, ", "),
				"source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte,
				"source_line": invocation.Span.Line, "source_column": invocation.Span.Column,
				"source_unit_id": invocation.SourceUnitID,
			}}
		}
	}
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"tool": "command"}}
}
