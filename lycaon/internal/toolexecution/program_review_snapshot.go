package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Boundary normalization may grow the original expanded object. Admit its
// actual final shape before review serialization, retaining that immutable
// object and its canonical encodings through the complete owner invocation.
// The resolved-secret copy stays with its existing ephemeral owner.
func admitReviewedOperationInputs(ctx context.Context, admission ProgramStateAdmission, canonical map[string]any) (*nativeArguments, toolcontract.ReviewedOperationInputs, error) {
	snapshot, err := prepareNativeArguments(ctx, admission, canonical)
	if err != nil {
		return nil, toolcontract.ReviewedOperationInputs{}, err
	}
	reviewed, release, err := toolcontract.NewReviewedOperationInputs(ctx, admission, snapshot.values)
	if err != nil {
		snapshot.close()
		return nil, toolcontract.ReviewedOperationInputs{}, err
	}
	snapshot.leases = append(snapshot.leases, release)
	return snapshot, reviewed, nil
}
