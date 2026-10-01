package secretcap

// ValidationError identifies an invalid request field without echoing its value.
type ValidationError struct {
	Kind   error
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Kind.Error() + ": " + e.Field + ": " + e.Reason }
func (e *ValidationError) Unwrap() error { return e.Kind }

func invalidField(kind error, field, reason string) error {
	return &ValidationError{Kind: kind, Field: field, Reason: reason}
}
