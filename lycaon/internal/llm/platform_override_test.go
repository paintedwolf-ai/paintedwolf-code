package llm

import (
	"os"
)

// SetHostProductPlatformForTest overrides the resolver during tests.
func SetHostProductPlatformForTest(platform string) (restore func()) {
	if os.Getenv("LYCAON_TEST") != "1" {
		panic("llm.SetHostProductPlatformForTest requires LYCAON_TEST=1")
	}
	prev := hostProductPlatform
	hostProductPlatform = func() string { return platform }
	return func() { hostProductPlatform = prev }
}
