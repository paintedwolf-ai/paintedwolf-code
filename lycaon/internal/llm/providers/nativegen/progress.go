package nativegen

import "errors"

type progressCursor struct {
	requestID uint64
	prompt    int
	processed int
	generated int
	reused    int
	maximum   int
	chunk     int
	cancel    bool
	terminal  bool
}

// validate settles one bounded step before its device permit is returned.
func (r *progressResult) validate() error {
	p, e := r.Progress, r.expected
	invalid := errors.New("native generation: inconsistent step progress")
	if e == nil || e.terminal || p.RequestID != e.requestID || p.ReusedPrefixTokens != e.reused || p.ProcessedPromptTokens < e.processed || p.ProcessedPromptTokens > e.prompt || p.ProcessedPromptTokens-e.processed > e.chunk || p.GeneratedTokens < e.generated || p.GeneratedTokens > e.maximum || p.GeneratedTokens-e.generated > 1 {
		return invalid
	}
	if p.Token != nil && *p.Token >= 262144 {
		return invalid
	}
	stop := ""
	if p.StopReason != nil {
		stop = *p.StopReason
	}
	if e.cancel {
		if stop != "canceled" || p.Token != nil || r.Text != "" || p.GeneratedTokens != e.generated || p.ProcessedPromptTokens != e.processed {
			return invalid
		}
	} else if p.ProcessedPromptTokens < e.prompt {
		if p.ProcessedPromptTokens == e.processed || p.GeneratedTokens != 0 || p.Token != nil || r.Text != "" || p.StopReason != nil {
			return invalid
		}
	} else {
		if p.GeneratedTokens != e.generated+1 {
			return invalid
		}
		switch stop {
		case "eos", "stop_token":
			if p.Token != nil {
				return invalid
			}
		case "max_tokens":
			if p.GeneratedTokens != e.maximum || p.Token == nil {
				return invalid
			}
		case "structured_output":
			if p.Token == nil {
				return invalid
			}
		case "":
			if p.StopReason != nil || p.Token == nil || p.GeneratedTokens == e.maximum {
				return invalid
			}
		default:
			return invalid
		}
	}
	e.processed, e.generated, e.terminal = p.ProcessedPromptTokens, p.GeneratedTokens, p.StopReason != nil
	return nil
}

func (r *resourceResult) validate() error { return r.Plan.validate() }
