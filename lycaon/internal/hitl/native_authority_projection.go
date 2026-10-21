package hitl

import (
	"context"
	"fmt"
	"reflect"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// NativeAuthorityProjection is a lossless durable authority description, never
// executable operation inputs. Restore does not grant or install its contents.
type NativeAuthorityProjection struct{ value toolcontract.NativeValue }

func (p NativeAuthorityProjection) Representation() string { return p.value.Representation() }
func (p NativeAuthorityProjection) Digest() string         { return p.value.Digest() }
func (p NativeAuthorityProjection) Canonical() string      { return p.value.Canonical() }
func (p NativeAuthorityProjection) Valid() bool {
	return p.value.Valid() && p.value.Representation() == "native_approval_authority_v1"
}

func NewNativeAuthorityProjection(ctx context.Context, state toolcontract.ReviewStateAdmission, options []ApprovalOption) (NativeAuthorityProjection, func(), error) {
	if ctx == nil || state == nil || len(options) == 0 {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("native authority requires actual options, context and admission")
	}
	binding := &authorityBinding{ctx: ctx, state: state}
	defer binding.close()
	tree, err := binding.value(reflect.ValueOf(options))
	if err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	if err := validateNativeAuthorityOptions(options); err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	value, release, err := toolcontract.NewNativeValue(ctx, state, toolcontract.ApprovalAuthorityDomain, tree)
	if err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	return NativeAuthorityProjection{value: value}, release, nil
}

// Options returns a detached typed authority tree under its own lease. The
// actual plan validator and current-policy installer remain mandatory callers.
func (p NativeAuthorityProjection) Options(ctx context.Context, state toolcontract.ReviewStateAdmission) ([]ApprovalOption, func(), error) {
	if ctx == nil || state == nil || !p.Valid() {
		return nil, nil, fmt.Errorf("known admitted native authority required")
	}
	tree, releaseTree, err := p.value.Value(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	binding := &authorityBinding{ctx: ctx, state: state}
	var options []ApprovalOption
	if err := binding.assign(reflect.ValueOf(&options).Elem(), tree); err != nil {
		binding.close()
		releaseTree()
		return nil, nil, err
	}
	if len(options) == 0 {
		binding.close()
		releaseTree()
		return nil, nil, fmt.Errorf("native authority options are empty")
	}
	if err := validateNativeAuthorityOptions(options); err != nil {
		binding.close()
		releaseTree()
		return nil, nil, err
	}
	releaseBinding := binding.release()
	return options, func() { releaseBinding(); releaseTree() }, nil
}

func validateNativeAuthorityOptions(options []ApprovalOption) error {
	for _, option := range options {
		if !validApprovalRung(option.Rung) {
			return fmt.Errorf("native authority option has unknown rung")
		}
		if err := option.validateKind(); err != nil {
			return err
		}
		if err := option.validateGrantIdentities(); err != nil {
			return err
		}
		if len(option.Authority) == 0 {
			return fmt.Errorf("native option has no authority description")
		}
		for _, delta := range option.Authority {
			if err := delta.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreNativeAuthorityProjection is the canonical native authority reader.
// Unknown domains, versions and host-field shapes fail before installation.
func RestoreNativeAuthorityProjection(ctx context.Context, state toolcontract.ReviewStateAdmission, representation, digest, canonical string) (NativeAuthorityProjection, func(), error) {
	value, release, err := toolcontract.RestoreNativeValue(ctx, state, toolcontract.ApprovalAuthorityDomain, representation, digest, canonical)
	if err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	projection := NativeAuthorityProjection{value: value}
	options, releaseOptions, err := projection.Options(ctx, state)
	if err != nil {
		release()
		return NativeAuthorityProjection{}, nil, err
	}
	// Rebinding verifies this exact declared typed shape, including timestamp
	// and numeric canonicality; the scalar codec remains the sole encoder.
	normalized, releaseNormalized, err := NewNativeAuthorityProjection(ctx, state, options)
	releaseOptions()
	if err != nil {
		release()
		return NativeAuthorityProjection{}, nil, err
	}
	if normalized.Canonical() != canonical {
		releaseNormalized()
		release()
		return NativeAuthorityProjection{}, nil, fmt.Errorf("noncanonical native authority host shape")
	}
	release()
	return normalized, releaseNormalized, nil
}
