package failure

type OutputProtocolFailureKind string

const (
	MalformedEnvelope OutputProtocolFailureKind = "malformed_envelope"
	DecodeMismatch    OutputProtocolFailureKind = "decode_mismatch"
	OutputBound       OutputProtocolFailureKind = "output_bound"
)

// ProviderOutputProtocolError identifies a rejected model-authored envelope.
type ProviderOutputProtocolError struct {
	ProviderID string
	Model      string
	Reason     string
	Kind       OutputProtocolFailureKind
	Cause      error
}

func (e *ProviderOutputProtocolError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "provider output protocol violation"
}

func (e *ProviderOutputProtocolError) Unwrap() error { return e.Cause }
