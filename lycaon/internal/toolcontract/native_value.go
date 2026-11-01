package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	writer := nativeValueWriter{ctx: ctx, state: state}
	defer writer.close()
	if err := writer.text(`["` + string(domain) + `",1,`); err != nil {
		return NativeValue{}, nil, err
	}
	if err := writer.value(value, 0); err != nil {
		return NativeValue{}, nil, err
	}
	if err := writer.text("]"); err != nil {
		return NativeValue{}, nil, err
	}
	retained := &reviewLeases{}
	if err := retained.reserve(ctx, state, int64(len(writer.bytes))); err != nil {
		return NativeValue{}, nil, err
	}
	sum := sha256.Sum256(writer.bytes)
	return NativeValue{domain: domain, digest: hex.EncodeToString(sum[:]), canonical: string(writer.bytes), valid: true}, retained.release(), nil
}

// Value decodes admitted containers directly from the canonical representation.
func (v NativeValue) Value(ctx context.Context, state ReviewStateAdmission) (any, func(), error) {
	if ctx == nil || state == nil || !v.Valid() {
		return nil, nil, fmt.Errorf("admitted native value required")
	}
	reader := nativeValueReader{ctx: ctx, state: state, source: v.canonical, leases: &reviewLeases{}}
	prefix := `["` + string(v.domain) + `",1,`
	if err := reader.take(prefix); err != nil {
		return nil, nil, err
	}
	value, err := reader.value(0)
	if err == nil {
		err = reader.take("]")
	}
	if err == nil && reader.at != len(reader.source) {
		err = fmt.Errorf("native value has trailing data")
	}
	if err != nil {
		reader.leases.close()
		return nil, nil, err
	}
	return value, reader.leases.release(), nil
}

// RestoreNativeValue returns validated canonical bytes and their admitted decoded tree.
func RestoreNativeValue(ctx context.Context, state ReviewStateAdmission, domain NativeValueDomain, representation, digest, canonical string) (NativeValue, any, func(), error) {
	if !domain.valid() || representation != string(domain)+"_v1" {
		return NativeValue{}, nil, nil, fmt.Errorf("unknown native value representation")
	}
	candidate := NativeValue{domain: domain, digest: digest, canonical: canonical, valid: true}
	value, releaseDecoded, err := candidate.Value(ctx, state)
	if err != nil {
		return NativeValue{}, nil, nil, err
	}
	sum := sha256.Sum256([]byte(canonical))
	if digest != hex.EncodeToString(sum[:]) {
		releaseDecoded()
		return NativeValue{}, nil, nil, fmt.Errorf("native value digest mismatch")
	}
	normalized, releaseEncoded, err := NewNativeValue(ctx, state, domain, value)
	if err != nil {
		releaseDecoded()
		return NativeValue{}, nil, nil, err
	}
	if normalized.canonical != canonical {
		releaseEncoded()
		releaseDecoded()
		return NativeValue{}, nil, nil, fmt.Errorf("noncanonical native value representation")
	}
	return normalized, value, func() { releaseEncoded(); releaseDecoded() }, nil
}
