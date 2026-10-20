package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/authzcontext"
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
	reviewed, err := freezeReviewedOperationInputs(snapshot.values)
	if err != nil {
		snapshot.close()
		return nil, toolcontract.ReviewedOperationInputs{}, err
	}
	return snapshot, reviewed, nil
}

func freezeReviewedOperationInputs(canonical map[string]any) (toolcontract.ReviewedOperationInputs, error) {
	encoded, err := authzcontext.CanonicalJSON(canonical)
	if err != nil {
		return toolcontract.ReviewedOperationInputs{}, err
	}
	digest, err := authzcontext.CanonicalDigest(canonical)
	if err != nil {
		return toolcontract.ReviewedOperationInputs{}, err
	}
	return toolcontract.ReviewedOperationInputs{ArgumentsDigest: digest, CanonicalArguments: string(encoded)}, nil
}
