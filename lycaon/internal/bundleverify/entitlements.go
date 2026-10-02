package bundleverify

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"slices"
)

const browserBinaryName = "chrome-headless-shell"

// entitlementContract is exactly what one shipped executable may carry.
// Runtime code generation is confined to the browser; every executable built
// from this repository runs under the full hardened runtime.
type entitlementContract struct {
	required []string
	allowed  []string
}

var (
	browserContract = entitlementContract{required: []string{
		"com.apple.security.cs.allow-jit",
		"com.apple.security.cs.allow-unsigned-executable-memory",
	}}
	// The engine helper's provisioning profile grants its Keychain identity;
	// checkCredentialProtection proves the profile itself.
	engineContract = entitlementContract{allowed: []string{
		"com.apple.application-identifier",
		"com.apple.developer.team-identifier",
		"keychain-access-groups",
	}}
)

func contractFor(rel string) entitlementContract {
	switch {
	case filepath.Base(rel) == browserBinaryName:
		return browserContract
	case rel == sidecarRelPath:
		return engineContract
	default:
		return entitlementContract{}
	}
}

// checkEntitlements holds every hardened executable to its contract.
func checkEntitlements(ctx context.Context, runner Runner, opts Options, machOs []MachOFacts, severity Severity) []Finding {
	var findings []Finding
	for _, m := range machOs {
		rel := bundleRel(opts.AppPath, m.Path)
		_, stderr, err := runner.Run(ctx, "codesign", "--display", "--verbose=2", m.Path)
		if err != nil {
			continue
		}
		// An executable without the hardened runtime is already a finding;
		// its entitlements would not constrain it.
		if flags, ok := parseCodesignFlags(stderr); !ok || flags&hardenedRuntimeFlag == 0 {
			continue
		}
		stdout, _, err := runner.Run(ctx, "codesign", "-d", "--entitlements", "-", "--xml", m.Path)
		if isToolMissing(err) {
			continue
		}
		var granted map[string]bool
		if err == nil {
			granted, err = parseEntitlements(stdout)
		}
		if err != nil {
			findings = append(findings, Finding{
				Code: CodeEntitlementUnreadable, Severity: severity, Path: rel,
				Detail: map[string]string{"reason": err.Error()},
			})
			continue
		}
		findings = append(findings, contractFor(rel).violations(granted, rel, severity)...)
	}
	return findings
}

func (c entitlementContract) violations(granted map[string]bool, rel string, severity Severity) []Finding {
	var findings []Finding
	for _, name := range c.required {
		if !granted[name] {
			findings = append(findings, Finding{
				Code: CodeEntitlementMissing, Severity: severity, Path: rel,
				Detail: map[string]string{"entitlement": name},
			})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(granted)) {
		if !slices.Contains(c.required, name) && !slices.Contains(c.allowed, name) {
			findings = append(findings, Finding{
				Code: CodeEntitlementUnexpected, Severity: severity, Path: rel,
				Detail: map[string]string{"entitlement": name},
			})
		}
	}
	return findings
}

// parseEntitlements reads the top-level keys of codesign's entitlements
// plist; each is true only for a boolean true value. No output is no
// entitlements.
func parseEntitlements(data []byte) (map[string]bool, error) {
	granted := map[string]bool{}
	if len(bytes.TrimSpace(data)) == 0 {
		return granted, nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	key, pending := "", false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return granted, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			// plist > dict > entries
			if depth != 3 {
				continue
			}
			if t.Name.Local == "key" {
				var name string
				if err := decoder.DecodeElement(&name, &t); err != nil {
					return nil, err
				}
				depth--
				key, pending = name, true
				continue
			}
			if pending {
				granted[key] = t.Name.Local == "true"
				pending = false
			}
		case xml.EndElement:
			depth--
		}
	}
}
