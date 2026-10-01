package bundleverify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkCredentialProtection exercises OS authorization using disposable identities.
func checkCredentialProtection(ctx context.Context, runner Runner, opts Options) []Finding {
	if !opts.RequireSigned {
		return nil
	}
	sidecar := filepath.Join(opts.AppPath, sidecarRelPath)
	profile := filepath.Join(filepath.Dir(filepath.Dir(sidecar)), "embedded.provisionprofile")
	if info, err := os.Lstat(profile); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return []Finding{{Code: CodeCredentialProtectionFailed, Severity: SeverityError, Path: bundleRel(opts.AppPath, profile),
			Detail: map[string]string{"reason": "engine helper requires a nonempty embedded provisioning profile"}}}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, stderr, err := runner.Run(ctx, sidecar, "credentials", "verify-protection")
	if err == nil {
		return nil
	}
	return []Finding{{Code: CodeCredentialProtectionFailed, Severity: SeverityError, Path: sidecarRelPath,
		Detail: map[string]string{"reason": err.Error(), "output": strings.TrimSpace(string(stderr))}}}
}
