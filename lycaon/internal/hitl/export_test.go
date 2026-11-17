package hitl

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// RestoreNativeAuthorityProjection is the canonical native authority reader.
// Unknown domains, versions and host-field shapes fail before installation.
func RestoreNativeAuthorityProjection(ctx context.Context, state toolcontract.ReviewStateAdmission, representation, digest, canonical string) (NativeAuthorityProjection, func(), error) {
	restored, release, err := restoreNativeAuthority(ctx, state, representation, digest, canonical)
	return restored.projection, release, err
}
