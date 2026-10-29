package codec

import (
	"bytes"
	"github.com/zricethezav/gitleaks/v8/detect/budget"

	"github.com/zricethezav/gitleaks/v8/logging"
)

// Decoder decodes various types of data in place
type Decoder struct {
	decodedMap map[string]string
	meter      *budget.Meter
}

// NewDecoder creates a default decoder struct
func NewDecoder() *Decoder {
	return &Decoder{
		decodedMap: make(map[string]string),
	}
}

// NewDecoderWithBudget retains the codec policy with explicit allocation/work admission.
func NewDecoderWithBudget(meter *budget.Meter) *Decoder {
	meter.Allocate(128)
	d := NewDecoder()
	d.meter = meter
	return d
}

// Decode returns the data with the values decoded in place along with the
// encoded segment meta data for the next pass of decoding
func (d *Decoder) Decode(data string, predecessors []*EncodedSegment) (string, []*EncodedSegment) {
	d.meter.Check()
	segments := d.findEncodedSegments(data, predecessors)

	if len(segments) > 0 {
		d.meter.Allocate(int64(len(data))*4 + 128)
		result := bytes.NewBuffer(make([]byte, 0, len(data)))
		encodedStart := 0
		for _, segment := range segments {
			result.WriteString(data[encodedStart:segment.encoded.start])
			result.WriteString(segment.decodedValue)
			encodedStart = segment.encoded.end
		}

		result.WriteString(data[encodedStart:])
		return result.String(), segments
	}

	return data, segments
}

// findEncodedSegments finds the encoded segments in the data
func (d *Decoder) findEncodedSegments(data string, predecessors []*EncodedSegment) []*EncodedSegment {
	if len(data) == 0 {
		return []*EncodedSegment{}
	}

	decodedShift := 0
	encodingMatches := findEncodingMatchesBudget(data, d.meter)
	d.meter.Allocate(int64(len(encodingMatches)) * 512)
	segments := make([]*EncodedSegment, 0, len(encodingMatches))
	for _, m := range encodingMatches {
		d.meter.Check()
		encodedValue := data[m.start:m.end]
		decodedValue, alreadyDecoded := d.decodedMap[encodedValue]

		if !alreadyDecoded {
			d.meter.Allocate(int64(len(encodedValue))*8 + 128)
			d.meter.Work(int64(len(encodedValue)))
			if m.encoding.kind == unicodeKind {
				decodedValue = decodeUnicodeBudget(encodedValue, d.meter)
			} else {
				decodedValue = m.encoding.decode(encodedValue)
			}
			d.decodedMap[encodedValue] = decodedValue
		}

		if len(decodedValue) == 0 {
			continue
		}

		segment := &EncodedSegment{
			predecessors: predecessors,
			original:     toOriginalBudget(predecessors, m.startEnd, d.meter),
			encoded:      m.startEnd,
			decoded: startEnd{
				m.start + decodedShift,
				m.start + decodedShift + len(decodedValue),
			},
			decodedValue: decodedValue,
			encodings:    m.encoding.kind,
			depth:        1,
		}

		// Shift decoded start and ends based on size changes
		decodedShift += len(decodedValue) - len(encodedValue)

		// Adjust depth and encoding if applicable
		if len(segment.predecessors) != 0 {
			// Set the depth based on the predecessors' depth in the previous pass
			segment.depth = 1 + segment.predecessors[0].depth
			// Adjust encodings
			for _, p := range segment.predecessors {
				d.meter.Work(1)
				if segment.encoded.overlaps(p.decoded) {
					segment.encodings |= p.encodings
				}
			}
		}

		segments = append(segments, segment)
		logging.Debug().
			Str("decoder", m.encoding.kind.String()).
			Msgf(
				"segment found: original=%s pos=%s: %q -> %q",
				segment.original,
				segment.encoded,
				encodedValue,
				segment.decodedValue,
			)
	}

	return segments
}
