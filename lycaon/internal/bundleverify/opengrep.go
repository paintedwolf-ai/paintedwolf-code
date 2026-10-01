package bundleverify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const engineRootRelPath = "Contents/Resources/engine-root"

func checkOpenGrep(ctx context.Context, runner Runner, opts Options) []Finding {
	sidecar := filepath.Join(opts.AppPath, sidecarRelPath)
	if info, err := os.Stat(sidecar); err != nil || info.Mode().Perm()&0o111 == 0 {
		return nil // The sidecar layout check reports this failure.
	}
	root := filepath.Join(opts.AppPath, engineRootRelPath)
	stdout, stderr, err := runner.Run(ctx, sidecar, "scan", "engines", "verify-bundled", "--root", root)
	if err != nil {
		code, severity := CodeOpenGrepInvalid, SeverityError
		var pathError *os.PathError
		if isToolMissing(err) || errors.As(err, &pathError) {
			code = CodeOpenGrepVerificationUnavailable
			if !opts.RequireSigned {
				severity = SeverityWarn
			}
		}
		return []Finding{{Code: code, Severity: severity, Path: engineRootRelPath,
			Detail: map[string]string{"reason": err.Error(), "output": strings.TrimSpace(string(stderr))}}}
	}
	path := strings.TrimSpace(string(stdout))
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsAbs(path) || !filepath.IsLocal(rel) ||
		!strings.HasPrefix(rel, "bundled"+string(filepath.Separator)) || filepath.Base(path) != "opengrep" {
		return []Finding{{Code: CodeOpenGrepInvalid, Severity: SeverityError, Path: engineRootRelPath,
			Detail: map[string]string{"reason": "sidecar returned an invalid scanner resource path"}}}
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedDirectory, directoryErr := filepath.EvalSymlinks(filepath.Dir(path))
	resolvedRelative, relativeErr := filepath.Rel(resolvedRoot, resolvedDirectory)
	if rootErr != nil || directoryErr != nil || relativeErr != nil || !filepath.IsLocal(resolvedRelative) {
		return []Finding{{Code: CodeOpenGrepInvalid, Severity: SeverityError, Path: engineRootRelPath,
			Detail: map[string]string{"reason": "scanner directory escapes application resources"}}}
	}
	return checkOpenGrepLayout(opts.AppPath, path)
}

func checkOpenGrepLayout(appPath, executable string) []Finding {
	var findings []Finding
	for _, name := range []string{"opengrep", "opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE"} {
		path := filepath.Join(filepath.Dir(executable), name)
		info, err := os.Lstat(path)
		reason := ""
		switch {
		case err != nil:
			reason = "not found"
		case !info.Mode().IsRegular() || info.Size() == 0:
			reason = "must be a nonempty regular file"
		case name == "opengrep" && info.Mode().Perm()&0o111 == 0:
			reason = "not executable"
		}
		if reason != "" {
			findings = append(findings, Finding{Code: CodeOpenGrepInvalid, Severity: SeverityError,
				Path: bundleRel(appPath, path), Detail: map[string]string{"reason": reason}})
		}
	}
	return findings
}
