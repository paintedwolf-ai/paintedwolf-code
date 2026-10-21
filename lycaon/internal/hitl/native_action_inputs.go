package hitl

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// NativeActionReview retains exact review inputs under the command host's
// aggregate ledger through all held reviews and the complete owner invocation.
// It carries no grants and never resolves secrets or dispatches an operation.
type NativeActionReview struct {
	mu         sync.Mutex
	ctx        context.Context
	admission  toolcontract.ReviewStateAdmission
	projection toolcontract.OperationInspectionProjector
	releases   []func()
	inputs     []*NativeActionInputs
	closed     bool
}

// NativeActionInputs separates full reviewed arguments from the existing
// semantic grant projection. Its private strings are immutable borrowed facts.
type NativeActionInputs struct {
	lifetime  *NativeActionReview
	tool      string
	reviewed  toolcontract.ReviewedOperationInputs
	identity  toolcontract.ReviewedOperationInputs
	arguments map[string]any
}

func NewNativeActionReview(ctx context.Context, admission toolcontract.ReviewStateAdmission, projection toolcontract.OperationInspectionProjector) *NativeActionReview {
	return &NativeActionReview{ctx: ctx, admission: admission, projection: projection}
}

// Freeze follows host normalization and precedes every card. The existing
// scalar clone preserves owner values; encoding never becomes an owner input.
func (r *NativeActionReview) Freeze(ctx context.Context, tool string, args map[string]any) (map[string]any, *NativeActionInputs, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("native action review lifetime required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.admission == nil || tool == "" {
		return nil, nil, fmt.Errorf("native action review is closed or incomplete")
	}
	reviewed, releaseReviewed, err := toolcontract.NewReviewedOperationInputs(ctx, r.admission, args)
	if err != nil {
		return nil, nil, err
	}
	// The review reservation includes retained map/normalization copies before
	// they allocate; no second tree clone or grant normalization algorithm exists.
	frozen := jsonvalue.CloneMap(args)
	identity, releaseIdentity, err := toolcontract.NewReviewedOperationInputs(ctx, r.admission, grantIdentityArgs(tool, frozen))
	if err != nil {
		releaseReviewed()
		return nil, nil, err
	}
	input := &NativeActionInputs{lifetime: r, tool: tool, reviewed: reviewed, identity: identity, arguments: frozen}
	r.releases = append(r.releases, releaseReviewed, releaseIdentity)
	r.inputs = append(r.inputs, input)
	return frozen, input, nil
}

func (i *NativeActionInputs) Reviewed() (toolcontract.ReviewedOperationInputs, error) {
	if i == nil || i.lifetime == nil {
		return toolcontract.ReviewedOperationInputs{}, fmt.Errorf("native action review required")
	}
	i.lifetime.mu.Lock()
	defer i.lifetime.mu.Unlock()
	if i.lifetime.closed || !i.reviewed.Valid() {
		return toolcontract.ReviewedOperationInputs{}, fmt.Errorf("native action review is closed")
	}
	return i.reviewed, nil
}

func (i *NativeActionInputs) grantArguments(tool string) (string, bool) {
	if i == nil || i.lifetime == nil {
		return "", false
	}
	i.lifetime.mu.Lock()
	defer i.lifetime.mu.Unlock()
	if i.lifetime.closed || i.tool != tool || !i.identity.Valid() || !i.reviewed.Valid() {
		return "", false
	}
	return i.identity.Representation() + "\x00" + i.identity.CanonicalArguments(), true
}

func (r *NativeActionReview) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for _, input := range r.inputs {
		input.arguments = nil
		input.tool = ""
		input.reviewed = toolcontract.ReviewedOperationInputs{}
		input.identity = toolcontract.ReviewedOperationInputs{}
	}
	r.inputs = nil
	for i := len(r.releases) - 1; i >= 0; i-- {
		r.releases[i]()
	}
	r.releases = nil
	r.admission = nil
	r.ctx = nil
	r.projection = nil
}

// Inspection requires the bounded capture producer over original scalar bytes
// and field names before lossless encoding. The retained request context supplies
// cancellation only; all resource admission remains the explicit typed port.
// This map is presentation data, never executable or restored owner arguments.
func (i *NativeActionInputs) Inspection() (map[string]any, error) {
	if i == nil || i.lifetime == nil {
		return nil, fmt.Errorf("native review inputs required")
	}
	r := i.lifetime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx == nil {
		return nil, fmt.Errorf("native review lifetime closed")
	}
	if r.projection == nil {
		return nil, fmt.Errorf("native review requires bounded raw-value inspection producer")
	}
	screened, err := r.projectInspection(i.arguments)
	if err != nil {
		return nil, err
	}
	return i.encodeInspection(screened)
}

// encodeInspection runs under the retained review lock, after raw screening.
func (i *NativeActionInputs) encodeInspection(screened map[string]any) (map[string]any, error) {
	r := i.lifetime
	view, release, err := toolcontract.NewReviewedOperationInputs(r.ctx, r.admission, screened)
	if err != nil {
		return nil, err
	}
	r.releases = append(r.releases, release)
	return map[string]any{"representation": view.Representation(), "canonical_arguments": view.CanonicalArguments(), "arguments_digest": view.ArgumentsDigest(), "reviewed_arguments_digest": i.reviewed.ArgumentsDigest()}, nil
}

// JSON presentation must name an unrepresentable path without silently
// substituting U+FFFD. This string is display-only, never a permission target.
func reviewDisplayText(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	return "Bytes " + strconv.Quote(value)
}
