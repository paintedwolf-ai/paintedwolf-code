package tools

import "github.com/lycaon/lycaon/internal/secretcap"

// referenceEchoesInError rewrites resolved values a failing consumer echoed
// into its reject data or error text.
func referenceEchoesInError(secrets *secretcap.Resolution, err error) error {
	if err == nil {
		return nil
	}
	if reject := AsToolReject(err); reject != nil {
		if data, ok := secrets.ReferenceEchoesIn(reject.Data).(map[string]any); ok {
			reject.Data = data
		}
		return err
	}
	if text := secrets.ReferenceEchoes(err.Error()); text != err.Error() {
		return echoRewrittenError{text: text, err: err}
	}
	return err
}

// echoRewrittenError keeps the cause for classification while its text carries references.
type echoRewrittenError struct {
	text string
	err  error
}

func (e echoRewrittenError) Error() string { return e.text }

func (e echoRewrittenError) Unwrap() error { return e.err }
