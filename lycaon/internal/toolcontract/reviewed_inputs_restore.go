package toolcontract

import (
	"context"
	"fmt"
)

// Arguments restores exact original scalar bytes for inspection/screening.
// The caller holds the detached map's lease; live owners retain frozen inputs.
func (r ReviewedOperationInputs) Arguments(ctx context.Context, admission ReviewStateAdmission) (map[string]any, func(), error) {
	if !r.Valid() {
		return nil, nil, fmt.Errorf("admitted operation inputs required")
	}
	value, release, err := r.value.Value(ctx, admission)
	if err != nil {
		return nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		release()
		return nil, nil, fmt.Errorf("operation argument root must be an object")
	}
	return object, release, nil
}

// RestoreReviewedOperationInputs accepts only the executable-input domain and
// object shape. Other native value domains cannot acquire operation meaning.
func RestoreReviewedOperationInputs(ctx context.Context, admission ReviewStateAdmission, representation, digest, canonical string) (ReviewedOperationInputs, func(), error) {
	value, object, release, err := RestoreNativeValue(ctx, admission, OperationArgumentsDomain, representation, digest, canonical)
	if err != nil {
		return ReviewedOperationInputs{}, nil, err
	}
	if _, ok := object.(map[string]any); !ok {
		release()
		return ReviewedOperationInputs{}, nil, fmt.Errorf("operation argument root must be an object")
	}
	return ReviewedOperationInputs{value: value}, release, nil
}
