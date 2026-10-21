package hitl

import (
	"context"
	"encoding/json"
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
	return p.value.Valid() && p.value.Representation() == NativeAuthorityFormat
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

// NativeAuthorityFormat is the only durable native authority representation.
const NativeAuthorityFormat = "native_approval_authority_v1"

// nativeAuthorityDisplay derives the screened presentation copy of native
// authority. Every string passes through reviewDisplayText, so the copy is
// valid UTF-8 and deterministic: a reader re-derives it to bind display rows to
// the lossless authority. The copy is never installed.
func nativeAuthorityDisplay(ctx context.Context, state toolcontract.ReviewStateAdmission, options []ApprovalOption) ([]ApprovalOption, func(), error) {
	if ctx == nil || state == nil {
		return nil, nil, fmt.Errorf("native authority display requires context and admission")
	}
	binding := &authorityBinding{ctx: ctx, state: state}
	tree, err := binding.value(reflect.ValueOf(options))
	if err != nil {
		binding.close()
		return nil, nil, err
	}
	var display []ApprovalOption
	if err := binding.assign(reflect.ValueOf(&display).Elem(), screenAuthorityTree(tree)); err != nil {
		binding.close()
		return nil, nil, err
	}
	return display, binding.release(), nil
}

func screenAuthorityTree(value any) any {
	switch v := value.(type) {
	case string:
		return reviewDisplayText(v)
	case []any:
		for n := range v {
			v[n] = screenAuthorityTree(v[n])
		}
	case map[string]any:
		for key, item := range v {
			v[key] = screenAuthorityTree(item)
		}
	}
	return value
}

// sameAuthorityDisplay reports whether a stored display copy is exactly the
// screened form of the restored authority. Both sides are valid UTF-8 here.
func sameAuthorityDisplay(derived, stored any) (bool, error) {
	left, err := json.Marshal(derived)
	if err != nil {
		return false, err
	}
	right, err := json.Marshal(stored)
	if err != nil {
		return false, err
	}
	return string(left) == string(right), nil
}

// detachAuthority deep-copies plan options through the typed binding under
// the review's aggregate admission; the copy lives as long as the review.
func (i *NativeActionInputs) detachAuthority(options []ApprovalOption) ([]ApprovalOption, error) {
	if i == nil || i.lifetime == nil {
		return nil, fmt.Errorf("native approval plan requires reviewed inputs")
	}
	r := i.lifetime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx == nil || r.admission == nil {
		return nil, fmt.Errorf("native approval plan review is closed")
	}
	binding := &authorityBinding{ctx: r.ctx, state: r.admission}
	tree, err := binding.value(reflect.ValueOf(options))
	if err != nil {
		binding.close()
		return nil, err
	}
	var detached []ApprovalOption
	if err := binding.assign(reflect.ValueOf(&detached).Elem(), tree); err != nil {
		binding.close()
		return nil, err
	}
	r.releases = append(r.releases, binding.release())
	return detached, nil
}

// durableAuthority splits a native plan into its lossless projection and the
// screened display plan whose JSON the checkpoint row carries. The display
// plan has its own canonical identity and never reaches an installer.
func (i *NativeActionInputs) durableAuthority(plan *ApprovalPlan) (NativeAuthorityProjection, *ApprovalPlan, func(), error) {
	if i == nil || i.lifetime == nil || plan == nil || plan.native != i {
		return NativeAuthorityProjection{}, nil, nil, fmt.Errorf("native approval plan lost its lossless authority")
	}
	r := i.lifetime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx == nil || r.admission == nil {
		return NativeAuthorityProjection{}, nil, nil, fmt.Errorf("native approval plan review is closed")
	}
	projection, releaseProjection, err := NewNativeAuthorityProjection(r.ctx, r.admission, plan.Options)
	if err != nil {
		return NativeAuthorityProjection{}, nil, nil, err
	}
	options, releaseDisplay, err := nativeAuthorityDisplay(r.ctx, r.admission, plan.Options)
	if err != nil {
		releaseProjection()
		return NativeAuthorityProjection{}, nil, nil, err
	}
	release := func() { releaseDisplay(); releaseProjection() }
	display := *plan
	display.native, display.Options = nil, options
	if display.ID, err = display.canonicalID(); err == nil {
		err = display.Validate()
	}
	if err != nil {
		release()
		return NativeAuthorityProjection{}, nil, nil, err
	}
	return projection, &display, release, nil
}
