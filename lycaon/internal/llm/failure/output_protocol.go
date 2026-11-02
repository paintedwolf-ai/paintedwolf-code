package failure

// ProviderOutputProtocolError identifies a rejected model-authored envelope.
type ProviderOutputProtocolError struct {
	ProviderID string
	Model      string
	Reason     string
	Repairable bool
	Cause      error
}

func (e *ProviderOutputProtocolError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "provider output protocol violation"
}

func (e *ProviderOutputProtocolError) Unwrap() error { return e.Cause }
