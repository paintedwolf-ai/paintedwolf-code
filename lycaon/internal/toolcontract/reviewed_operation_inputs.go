package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

const LosslessOperationArgumentsV1 = "operation_arguments_v1"

// ReviewedOperationInputs is an admitted immutable lossless opaque-reference
// object. Resolved secret values remain solely with the ephemeral owner.
// The returned lease must remain held while any consumer retains its strings.
type ReviewedOperationInputs struct {
	representation string
	digest         string
	canonical      string
	valid          bool
}

func (r ReviewedOperationInputs) Representation() string     { return r.representation }
func (r ReviewedOperationInputs) ArgumentsDigest() string    { return r.digest }
func (r ReviewedOperationInputs) CanonicalArguments() string { return r.canonical }
func (r ReviewedOperationInputs) Valid() bool {
	return r.valid && r.representation == LosslessOperationArgumentsV1
}

// ReviewStateAdmission borrows the host aggregate state ledger. It creates no
// independent allocator, memory allowance, or permission authority.
type ReviewStateAdmission interface {
	ReserveProgramState(context.Context, int64) (func(), error)
}

// OperationInspectionProjector is the host's bounded raw-value capture port.
// It screens original scalar bytes and field labels before encoding, returning
// a lease held through review/publication. Implementations use existing capture
// screening and stream/state budgets; this interface creates no allocator.
type OperationInspectionProjector interface {
	ProjectOperationInputs(context.Context, ReviewStateAdmission, map[string]any) (map[string]any, func(), error)
}

type reviewLeases struct{ releases []func() }

func (l *reviewLeases) reserve(ctx context.Context, admission ReviewStateAdmission, payload int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if payload < 0 || payload > (math.MaxInt64-2048)/16 {
		return fmt.Errorf("review argument memory bound overflow")
	}
	release, err := admission.ReserveProgramState(ctx, 2048+16*payload)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("review admission returned no retained lease")
	}
	l.releases = append(l.releases, release)
	return nil
}
func (l *reviewLeases) close() {
	for i := len(l.releases) - 1; i >= 0; i-- {
		l.releases[i]()
	}
	l.releases = nil
}
func (l *reviewLeases) release() func() { var once sync.Once; return func() { once.Do(l.close) } }

// NewReviewedOperationInputs encodes original scalar bytes and raw sorted keys.
// Every traversal frame and retained payload is admitted before descent/copy.
func NewReviewedOperationInputs(ctx context.Context, admission ReviewStateAdmission, arguments map[string]any) (ReviewedOperationInputs, func(), error) {
	if ctx == nil || admission == nil || arguments == nil {
		return ReviewedOperationInputs{}, nil, fmt.Errorf("review inputs require context, admission and argument object")
	}
	leases := &reviewLeases{}
	encoded, err := encodeReviewValue(ctx, admission, leases, arguments)
	if err != nil {
		leases.close()
		return ReviewedOperationInputs{}, nil, err
	}
	raw, err := json.Marshal([]any{"operation_arguments", 1, encoded})
	if err != nil {
		leases.close()
		return ReviewedOperationInputs{}, nil, err
	}
	sum := sha256.Sum256(raw)
	return ReviewedOperationInputs{representation: LosslessOperationArgumentsV1, digest: hex.EncodeToString(sum[:]), canonical: string(raw), valid: true}, leases.release(), nil
}
