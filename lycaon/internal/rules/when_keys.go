package rules

import (
	"sync"
)

var (
	whenKeysOnce sync.Once
	whenKeys     []string
	whenKeysErr  error
)
