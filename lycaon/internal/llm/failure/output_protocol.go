package failure

type OutputProtocolFailureKind string

const (
	MalformedEnvelope OutputProtocolFailureKind = "malformed_envelope"
	DecodeMismatch    OutputProtocolFailureKind = "decode_mismatch"
	OutputBound       OutputProtocolFailureKind = "output_bound"
)

// ProviderOutputProtocolError identifies an output boundary failure.
type ProviderOutputProtocolError struct {
	ProviderID string
	Model      string
	Reason     string
	Kind       OutputProtocolFailureKind
	Cause      error
}

func (e *ProviderOutputProtocolError) Error() string {
	if e != nil && e.Cause != nil {
		return e.Cause.Error()
	}
	return "provider output protocol violation"
}

func (e *ProviderOutputProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
