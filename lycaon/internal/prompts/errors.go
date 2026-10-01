package prompts

import "errors"

// ErrUnknownAgent is returned when persona contract lookup fails for an agent id.
var ErrUnknownAgent = errors.New("unknown agent")
