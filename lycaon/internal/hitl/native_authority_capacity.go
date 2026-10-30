package hitl

import (
	"context"
	"fmt"
	"reflect"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// admitAuthorityLifecycle verifies every offered choice before publishing the card.
func admitAuthorityLifecycle(ctx context.Context, projection NativeAuthorityProjection, display []ApprovalOption) error {
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		return err
	}
	memory, err := toolcontract.NewProgramMemory(limits)
	if err != nil {
		return err
	}
	// The measured SQL read coexists with the restored authority.
	releaseMargin, err := memory.ReserveProgramState(ctx, 2048+int64(len(projection.Canonical())))
	if err != nil {
		return err
	}
	defer releaseMargin()
	restored, release, err := restoreNativeAuthority(ctx, memory, projection.Representation(), projection.Digest(), projection.Canonical())
	if err != nil {
		return fmt.Errorf("approval cannot be answered: %w", err)
	}
	defer release()
	if err := validateNativeAuthorityDisplay(ctx, memory, restored.options, display); err != nil {
		return err
	}
	for n, option := range restored.options {
		_, _, releaseOption, err := prepareNativeOption(ctx, memory, option, display[n], Resolution{PersonID: "00000000-0000-4000-8000-000000000001"})
		if err != nil {
			return fmt.Errorf("approval choice cannot be answered: %w", err)
		}
		releaseOption()
	}

	return nil
}

// stampNativeOption admits the copies that carry the deciding person's provenance.
func stampNativeOption(ctx context.Context, state toolcontract.ReviewStateAdmission, option ApprovalOption, resolution Resolution) (ApprovalOption, func(), error) {
	release, err := reserveAuthorityCopy(ctx, state, option.Authority)
	if err != nil {
		return ApprovalOption{}, nil, err
	}
	return option.grantedBy(resolution), release, nil
}

func reserveAuthorityCopy(ctx context.Context, state toolcontract.ReviewStateAdmission, authority []ApprovalAuthorityDelta) (func(), error) {
	binding := &authorityBinding{ctx: ctx, state: state}
	size, err := authorityStorageBytes(reflect.TypeFor[ApprovalAuthorityDelta]().Size())
	if err != nil {
		return nil, err
	}
	if err := binding.reserve(int64(len(authority)) * size); err != nil {
		return nil, err
	}
	grantBytes, err := authorityStorageBytes(reflect.TypeFor[ApprovalGrant]().Size())
	if err != nil {
		binding.close()
		return nil, err
	}
	for _, delta := range authority {
		if delta.Grant != nil {
			if err := binding.reserve(grantBytes); err != nil {
				binding.close()
				return nil, err
			}
		}
	}
	return binding.release(), nil
}

func prepareNativeOption(ctx context.Context, state toolcontract.ReviewStateAdmission, option, display ApprovalOption, resolution Resolution) (ApprovalOption, approvedAuthority, func(), error) {
	releases := releaseList{}
	fail := func(err error) (ApprovalOption, approvedAuthority, func(), error) {
		releases.release()
		return ApprovalOption{}, approvedAuthority{}, nil, err
	}
	stamped, releaseStamp, err := stampNativeOption(ctx, state, option, resolution)
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseStamp)
	shown, releaseDisplay, err := stampNativeOption(ctx, state, display, resolution)
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseDisplay)
	projection, releaseProjection, err := NewNativeAuthorityProjection(ctx, state, []ApprovalOption{stamped})
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseProjection)
	return stamped, approvedAuthority{display: shown, native: projection}, releases.release, nil
}
