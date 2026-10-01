package bundleverify

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

func signingSeverity(requireSigned bool) Severity {
	if requireSigned {
		return SeverityError
	}
	return SeverityWarn
}

const browserBinaryName = "chrome-headless-shell"

var browserRequiredEntitlements = []string{
	"com.apple.security.cs.allow-jit",
	"com.apple.security.cs.allow-unsigned-executable-memory",
}

func checkSigning(ctx context.Context, runner Runner, opts Options, machOs []MachOFacts) []Finding {
	severity := signingSeverity(opts.RequireSigned)

	targets := make([]string, 0, len(machOs)+1)
	targets = append(targets, opts.AppPath)
	for _, m := range machOs {
		targets = append(targets, m.Path)
	}

	var findings []Finding
	toolMissing := map[string]bool{}

	for _, target := range targets {
		f, missing := checkCodesign(ctx, runner, opts, target, severity)
		if missing != "" {
			toolMissing[missing] = true
			continue
		}
		findings = append(findings, f...)
	}

	if f, missing := checkDeepVerify(ctx, runner, opts, severity); missing != "" {
		toolMissing[missing] = true
	} else {
		findings = append(findings, f...)
	}

	findings = append(findings, checkBrowserEntitlements(ctx, runner, opts, machOs, severity)...)

	// Validate the staple on the distributed container.
	stapleTarget := opts.AppPath
	if opts.DMGPath != "" {
		stapleTarget = opts.DMGPath
	}
	if f, missing := checkStaple(ctx, runner, opts, stapleTarget, severity); missing != "" {
		toolMissing[missing] = true
	} else {
		findings = append(findings, f...)
	}

	// Assess both the container and its application payload.
	assessTargets := []string{opts.AppPath}
	if opts.DMGPath != "" {
		assessTargets = append(assessTargets, opts.DMGPath)
	}
	for _, target := range assessTargets {
		if f, missing := checkGatekeeper(ctx, runner, opts, target, severity); missing != "" {
			toolMissing[missing] = true
		} else {
			findings = append(findings, f...)
		}
	}

	toolReason := "not available on this host; structural checks still ran"
	if opts.RequireSigned {
		toolReason = "not available on this host; --require-signed cannot verify the bundle"
	}
	for tool := range toolMissing {
		findings = append(findings, Finding{
			Code: CodeToolUnavailable, Severity: severity, Path: bundleRel(opts.AppPath, opts.AppPath),
			Detail: map[string]string{"tool": tool, "reason": toolReason},
		})
	}

	return findings
}

func checkCodesign(ctx context.Context, runner Runner, opts Options, target string, severity Severity) ([]Finding, string) {
	rel := bundleRel(opts.AppPath, target)

	_, stderr, err := runner.Run(ctx, "codesign", "--display", "--verbose=2", target)
	if isToolMissing(err) {
		return nil, "codesign"
	}

	var findings []Finding
	if err != nil {
		return append(findings, Finding{
			Code: CodeSignatureAdhoc, Severity: severity, Path: rel,
			Detail: map[string]string{"reason": "codesign --display failed"},
		}), ""
	}

	flags, haveFlags := parseCodesignFlags(stderr)
	if isAdhocSignature(stderr, flags, haveFlags) {
		findings = append(findings, Finding{
			Code: CodeSignatureAdhoc, Severity: severity, Path: rel,
		})
	}
	if !haveFlags || flags&hardenedRuntimeFlag == 0 {
		findings = append(findings, Finding{
			Code: CodeHardenedRuntimeMissing, Severity: severity, Path: rel,
		})
	}
	return findings, ""
}

// checkDeepVerify validates every nested seal.
func checkDeepVerify(ctx context.Context, runner Runner, opts Options, severity Severity) ([]Finding, string) {
	_, _, err := runner.Run(ctx, "codesign", "--verify", "--deep", "--strict", opts.AppPath)
	if isToolMissing(err) {
		return nil, "codesign"
	}
	if err != nil {
		return []Finding{{
			Code: CodeSignatureBroken, Severity: severity, Path: bundleRel(opts.AppPath, opts.AppPath),
			Detail: map[string]string{"reason": "codesign --verify --deep --strict failed"},
		}}, ""
	}
	return nil, ""
}

// checkBrowserEntitlements validates signed browser execution permissions.
func checkBrowserEntitlements(ctx context.Context, runner Runner, opts Options, machOs []MachOFacts, severity Severity) []Finding {
	var findings []Finding

	for _, m := range machOs {
		if filepath.Base(m.Path) != browserBinaryName {
			continue
		}
		rel := bundleRel(opts.AppPath, m.Path)

		_, stderr, err := runner.Run(ctx, "codesign", "--display", "--verbose=2", m.Path)
		if err != nil {
			continue
		}
		flags, haveFlags := parseCodesignFlags(stderr)
		if !haveFlags || flags&hardenedRuntimeFlag == 0 {
			continue
		}

		stdout, _, err := runner.Run(ctx, "codesign", "-d", "--entitlements", "-", "--xml", m.Path)
		if isToolMissing(err) {
			continue
		}
		if err != nil {
			findings = append(findings, Finding{
				Code: CodeBrowserEntitlementMissing, Severity: severity, Path: rel,
				Detail: map[string]string{"reason": "entitlements query failed"},
			})
			continue
		}
		for _, ent := range browserRequiredEntitlements {
			if !entitlementEnabled(stdout, ent) {
				findings = append(findings, Finding{
					Code: CodeBrowserEntitlementMissing, Severity: severity, Path: rel,
					Detail: map[string]string{"entitlement": ent},
				})
			}
		}
	}

	return findings
}

// entitlementEnabled matches a boolean entitlement entry.
func entitlementEnabled(data []byte, name string) bool {
	token := "<key>" + name + "</key>"
	idx := strings.Index(string(data), token)
	if idx < 0 {
		return false
	}
	rest := strings.TrimLeft(string(data)[idx+len(token):], " \t\r\n")
	return strings.HasPrefix(rest, "<true/>")
}

func checkStaple(ctx context.Context, runner Runner, opts Options, target string, severity Severity) ([]Finding, string) {
	if _, _, err := runner.Run(ctx, "xcrun", "stapler", "validate", target); err != nil {
		if isToolMissing(err) {
			return nil, "xcrun"
		}
		return []Finding{{
			Code: CodeNotStapled, Severity: severity, Path: bundleRel(opts.AppPath, target),
		}}, ""
	}
	return nil, ""
}

func checkGatekeeper(ctx context.Context, runner Runner, opts Options, target string, severity Severity) ([]Finding, string) {
	assessType := "exec"
	if opts.DMGPath != "" && target == opts.DMGPath {
		assessType = "open"
	}
	if _, _, err := runner.Run(ctx, "spctl", "--assess", "--type", assessType,
		"--context", "context:primary-signature", "-vv", target); err != nil {
		if isToolMissing(err) {
			return nil, "spctl"
		}
		return []Finding{{
			Code: CodeGatekeeperRejected, Severity: severity, Path: bundleRel(opts.AppPath, target),
		}}, ""
	}
	return nil, ""
}

func isToolMissing(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, exec.ErrNotFound)
}
