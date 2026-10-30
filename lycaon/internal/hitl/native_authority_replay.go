package hitl

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// approvedOption binds the saved native option to its display copy.
func (s authoritySnapshot) approvedOption(ctx context.Context, state toolcontract.ReviewStateAdmission, source authoritySource, display ApprovalOption) (ApprovalOption, func(), error) {
	projection, releaseProjection, err := s.read(ctx, state, source)
	if err != nil {
		return ApprovalOption{}, nil, err
	}
	options := projection.options
	if len(options) != 1 {
		releaseProjection()
		return ApprovalOption{}, nil, fmt.Errorf("%s: expected one approved option", source.name)
	}
	if err := validateNativeAuthorityDisplay(ctx, state, options, []ApprovalOption{display}); err != nil {
		releaseProjection()
		return ApprovalOption{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	return options[0], releaseProjection, nil
}

// chatGrant restores the complete revocation group bound to its saved display.
func (s authoritySnapshot) chatGrant(ctx context.Context, state toolcontract.ReviewStateAdmission, chatSessionID, id string, display ApprovalAuthorityDelta) ([]ApprovalAuthorityDelta, func(), error) {
	source := chatGrantAuthoritySource(chatSessionID, id)
	restored, release, err := s.read(ctx, state, source)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) ([]ApprovalAuthorityDelta, func(), error) {
		release()
		return nil, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	if len(restored.options) != 1 {
		return fail(fmt.Errorf("expected one approved option"))
	}
	option := restored.options[0]
	last := -1
	for n, delta := range option.Authority {
		if chat, grantID, ok := delta.ChatLifetime(); ok && chat == chatSessionID && grantID == id {
			last = n
		}
	}
	if last < 0 {
		return fail(fmt.Errorf("native authority does not name this grant"))
	}
	derived, releaseDerived, err := nativeAuthorityDisplay(ctx, state, restored.options)
	if err != nil {
		return fail(err)
	}
	same, err := sameAuthorityDisplay(derived[0].Authority[last], display)
	releaseDerived()
	if err != nil {
		return fail(err)
	}
	if !same {
		return fail(fmt.Errorf("native authority does not match its displayed grant"))
	}
	// The saved row carries the last delta written for this revocation identity.
	group := option.Authority[:0]
	for _, delta := range option.Authority {
		if chat, grantID, ok := delta.ChatLifetime(); ok && chat == chatSessionID && grantID == id {
			group = append(group, delta)
		}
	}
	return group, release, nil
}
