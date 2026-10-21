package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

type NativeValueDomain string

const (
	OperationArgumentsDomain   NativeValueDomain = "operation_arguments"
	ApprovalAuthorityDomain    NativeValueDomain = "native_approval_authority"
	DiagnosticInspectionDomain NativeValueDomain = "native_diagnostic_inspection"
)

func (d NativeValueDomain) valid() bool {
	return d == OperationArgumentsDomain || d == ApprovalAuthorityDomain || d == DiagnosticInspectionDomain
}

// NativeValue is an admitted lossless value with a closed semantic domain.
// Its opaque representation alone grants no authority or replay permission.
type NativeValue struct {
	domain            NativeValueDomain
	digest, canonical string
	valid             bool
}

func (v NativeValue) Representation() string {
	if !v.Valid() {
		return ""
	}
	return string(v.domain) + "_v1"
}
func (v NativeValue) Digest() string    { return v.digest }
func (v NativeValue) Canonical() string { return v.canonical }
func (v NativeValue) Valid() bool       { return v.valid && v.domain.valid() }

// NewNativeValue admits each traversal frame before encoding original bytes.
// Callers retain its lease while any consumer retains the encoded value.
func NewNativeValue(ctx context.Context, state ReviewStateAdmission, domain NativeValueDomain, value any) (NativeValue, func(), error) {
	if ctx == nil || state == nil || !domain.valid() {
		return NativeValue{}, nil, fmt.Errorf("native value requires context, admission and a known domain")
	}
	leases := &reviewLeases{}
	encoded, err := encodeReviewValue(ctx, state, leases, value)
	if err != nil {
		leases.close()
		return NativeValue{}, nil, err
	}
	raw, err := json.Marshal([]any{string(domain), 1, encoded})
	if err != nil {
		leases.close()
		return NativeValue{}, nil, err
	}
	sum := sha256.Sum256(raw)
	return NativeValue{domain: domain, digest: hex.EncodeToString(sum[:]), canonical: string(raw), valid: true}, leases.release(), nil
}

// Value decodes only this value's exact versioned domain. The detached raw
// value has its own lease and is never silently interpreted as owner inputs.
func (v NativeValue) Value(ctx context.Context, state ReviewStateAdmission) (any, func(), error) {
	if ctx == nil || state == nil || !v.Valid() {
		return nil, nil, fmt.Errorf("admitted native value required")
	}
	leases := &reviewLeases{}
	if int64(len(v.canonical)) > (math.MaxInt64-2048)/64 {
		return nil, nil, fmt.Errorf("native value restore bound overflow")
	}
	if err := leases.reserve(ctx, state, int64(len(v.canonical))*4); err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(v.canonical))
	decoder.UseNumber()
	var envelope []any
	if err := decoder.Decode(&envelope); err != nil {
		leases.close()
		return nil, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		leases.close()
		return nil, nil, fmt.Errorf("native value has trailing data")
	}
	if len(envelope) != 3 || envelope[0] != string(v.domain) || envelope[1] != json.Number("1") {
		leases.close()
		return nil, nil, fmt.Errorf("unknown native value envelope")
	}
	value, err := decodeReviewValue(ctx, state, leases, envelope[2])
	if err != nil {
		leases.close()
		return nil, nil, err
	}
	return value, leases.release(), nil
}

// RestoreNativeValue verifies persisted version, domain, digest and canonical
// grammar under admission before hashing/copying. Domain confusion refuses.
func RestoreNativeValue(ctx context.Context, state ReviewStateAdmission, domain NativeValueDomain, representation, digest, canonical string) (NativeValue, func(), error) {
	if !domain.valid() || representation != string(domain)+"_v1" {
		return NativeValue{}, nil, fmt.Errorf("unknown native value representation")
	}
	candidate := NativeValue{domain: domain, digest: digest, canonical: canonical, valid: true}
	value, releaseDecoded, err := candidate.Value(ctx, state)
	if err != nil {
		return NativeValue{}, nil, err
	}
	sum := sha256.Sum256([]byte(canonical))
	if digest != hex.EncodeToString(sum[:]) {
		releaseDecoded()
		return NativeValue{}, nil, fmt.Errorf("native value digest mismatch")
	}
	normalized, releaseEncoded, err := NewNativeValue(ctx, state, domain, value)
	if err != nil {
		releaseDecoded()
		return NativeValue{}, nil, err
	}
	if normalized.canonical != canonical {
		releaseEncoded()
		releaseDecoded()
		return NativeValue{}, nil, fmt.Errorf("noncanonical native value representation")
	}
	return normalized, func() { releaseEncoded(); releaseDecoded() }, nil
}
