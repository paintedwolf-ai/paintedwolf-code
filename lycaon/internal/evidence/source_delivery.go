package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SourceDelivery is an actual delivered exact source window. Retained output and
// capture pins alone never construct this fact or advance an editor read basis.
type SourceDelivery struct {
	ResponseID      string                 `json:"response_id"`
	DeliveryID      string                 `json:"delivery_id"`
	Source          toolcontract.SourcePin `json:"source"`
	SourceStartByte int64                  `json:"source_start_byte"`
	SourceEndByte   int64                  `json:"source_end_byte"`
	StartLine       int                    `json:"start_line"`
	EndLine         int                    `json:"end_line"`
}

// DeliveredSourceObservation is constructed only after comparing actual emitted
// bytes with the exact captured representation. It contains no unobserved body.
type DeliveredSourceObservation struct{ record Record }

func (o DeliveredSourceObservation) Record() Record { return cloneRecord(o.record) }

// ConfirmDeliveredSource is called by the delivering host with actual response
// and delivery identities, an exact clipped mapping and its native capture. A
// transformed string or an external printed path cannot pass byte comparison.
// Input buffers and decoder/snapshot memory remain admitted by the caller.
func ConfirmDeliveredSource(path string, pin toolcontract.SourcePin, captured, delivered []byte, mapping toolcontract.StreamSourceMapping, responseID, deliveryID string) (DeliveredSourceObservation, error) {
	var out DeliveredSourceObservation
	if path == "" || responseID == "" || deliveryID == "" || len(delivered) == 0 || len(delivered) > EvidenceCaptureBodyCapBytes || pin.StartByte != 0 || pin.EndByte != int64(len(captured)) || mapping.Source != pin {
		return out, fmt.Errorf("delivered source requires exact native capture and response identity")
	}
	switch pin.Representation {
	case toolcontract.UTF8SourceBytes, toolcontract.LogicalUTF8LF, toolcontract.OriginalEncodedBytes:
	default:
		return out, fmt.Errorf("unknown captured source representation")
	}
	if !utf8.Valid(captured) || !utf8.Valid(delivered) {
		return out, fmt.Errorf("source window is not exact UTF-8 text")
	}
	digest := sha256.Sum256(captured)
	if pin.SHA256 != hex.EncodeToString(digest[:]) || pin.Revision == "" || pin.SourceID == "" {
		return out, fmt.Errorf("captured source identity mismatch")
	}
	start, end := mapping.SourceStartByte, mapping.SourceEndByte
	if start < 0 || end < start || end > int64(len(captured)) || mapping.OutputStartByte != 0 || mapping.OutputEndByte != int64(len(delivered)) || end-start != int64(len(delivered)) {
		return out, fmt.Errorf("invalid delivered source window")
	}
	if !bytes.Equal(captured[start:end], delivered) || !utf8.Valid(captured[:start]) {
		return out, fmt.Errorf("delivered bytes are transformed or split a source character")
	}
	lines := strings.Split(string(delivered), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := 1 + bytes.Count(captured[:start], []byte{'\n'})
	last := first + len(lines) - 1
	fact := SourceDelivery{ResponseID: responseID, DeliveryID: deliveryID, Source: pin, SourceStartByte: start, SourceEndByte: end, StartLine: first, EndLine: last}
	out.record = Record{Kind: "read", Shape: ShapeFileRegion, Fidelity: FidelityStructured, SourceTool: "command", Path: path, LineRanges: []LineRange{{Start: first, End: last}}, Body: lines, SourceDelivery: &fact}
	return out, nil
}

func verifyDeliveredSource(rec Record, claim Claim) (bool, string) {
	fact := rec.SourceDelivery
	if fact == nil || fact.ResponseID == "" || fact.DeliveryID == "" || fact.StartLine < 1 || fact.EndLine < fact.StartLine {
		return false, ""
	}
	if claim.Path != "" && NormalizeLedgerPath(claim.Path) != NormalizeLedgerPath(rec.Path) {
		return false, ""
	}
	excerpt := strings.TrimSpace(claim.Excerpt)
	if claim.Line > 0 {
		if claim.Line < fact.StartLine || claim.Line > fact.EndLine {
			return false, ""
		}
		if excerpt == "" {
			return true, ""
		}
		index := claim.Line - fact.StartLine
		return index < len(rec.Body) && strings.Contains(rec.Body[index], excerpt), ""
	}
	if excerpt == "" {
		return true, ""
	}
	return excerptInRecordBodies(rec, excerpt), ""
}

// RecordStateBytes measures the detached observation copy without allocating it.
// Delivery consumers reserve this before Record; input capture and the original
// opaque observation remain independently retained by their producing owner.
func (o DeliveredSourceObservation) RecordStateBytes() (int64, error) {
	total := int64(4096)
	add := func(n int) error {
		if int64(n) > (int64(^uint64(0)>>1)-total-256)/8 {
			return fmt.Errorf("source delivery metadata bound overflow")
		}
		total += int64(n)*8 + 256
		return nil
	}
	for _, text := range []string{o.record.Path, o.record.Kind, o.record.SourceTool, o.record.Handle} {
		if err := add(len(text)); err != nil {
			return 0, err
		}
	}
	for _, line := range o.record.Body {
		if err := add(len(line)); err != nil {
			return 0, err
		}
	}
	if fact := o.record.SourceDelivery; fact != nil {
		for _, text := range []string{fact.ResponseID, fact.DeliveryID, fact.Source.SourceID, fact.Source.Revision, fact.Source.SHA256, string(fact.Source.Representation)} {
			if err := add(len(text)); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}
