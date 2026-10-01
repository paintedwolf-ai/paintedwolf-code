package preflight

import "context"

// DefaultEnv builds a production Env. Callers supply the facts preflight cannot
// resolve on its own (config dir, provider count, scanner resolver, browser
// usability check, and decision engine status); everything else comes from the host.
func DefaultEnv(
	configDir string,
	providerCount func() int,
	missingRoleProviders func() map[string]string,
	resolveScanner func() (string, string, error),
	checkBrowser func(context.Context) (BrowserReason, error),
	checkDecisionEngine func(context.Context) DecisionReason,
) Env {
	return Env{
		ConfigDir:            configDir,
		FreeBytes:            freeBytes,
		OSProductVer:         osProductVersion,
		ProviderCount:        providerCount,
		MissingRoleProviders: missingRoleProviders,
		ResolveScanner:       resolveScanner,
		CheckBrowser:         checkBrowser,
		CheckDecisionEngine:  checkDecisionEngine,
	}
}
