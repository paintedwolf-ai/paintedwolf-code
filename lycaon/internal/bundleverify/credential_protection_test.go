package bundleverify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialProtectionRequiresSuccessfulSignedProbe(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	engine := filepath.Join(app, sidecarRelPath)
	for _, signed := range []bool{false, true} {
		runner := signedOK()
		runner.err[engine+" credentials"] = errors.New("missing entitlement")
		findings := checkCredentialProtection(context.Background(), runner, Options{AppPath: app, RequireSigned: signed})
		if signed {
			if len(findings) != 1 || findings[0].Code != CodeCredentialProtectionFailed || findings[0].Severity != SeverityError {
				t.Fatalf("probe failure findings: %+v", findings)
			}
			calls := runner.callsFor(engine)
			if len(calls) != 1 || len(calls[0]) != 3 || calls[0][1] != "credentials" || calls[0][2] != "verify-protection" {
				t.Fatalf("probe invocation: %v", calls)
			}
		} else if len(findings) != 0 || len(runner.calls) != 0 {
			t.Fatal("unsigned audit executed the protected host")
		}
	}
	runner := signedOK()
	if findings := checkCredentialProtection(context.Background(), runner, Options{AppPath: app, RequireSigned: true}); len(findings) != 0 {
		t.Fatalf("successful probe: %+v", findings)
	}
}

func TestCredentialProbeDoesNotRunAfterSignatureFailure(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	runner := signedOK()
	runner.err["codesign --verify"] = errors.New("broken signature")
	_, err := Verify(context.Background(), Options{AppPath: app, RequireSigned: true, Runner: runner})
	if err != nil {
		t.Fatalf("verify bundle: %v", err)
	}
	if calls := runner.callsFor(filepath.Join(app, sidecarRelPath)); len(calls) != 0 {
		t.Fatalf("executed unverified host: %v", calls)
	}
}

func TestCredentialProtectionRequiresEmbeddedProfile(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	profile := filepath.Join(app, filepath.Dir(filepath.Dir(sidecarRelPath)), "embedded.provisionprofile")
	if err := os.Remove(profile); err != nil {
		t.Fatalf("remove profile fixture: %v", err)
	}
	runner := signedOK()
	findings := checkCredentialProtection(context.Background(), runner, Options{AppPath: app, RequireSigned: true})
	if len(findings) != 1 || findings[0].Code != CodeCredentialProtectionFailed {
		t.Fatalf("missing profile findings: %+v", findings)
	}
	if len(runner.calls) != 0 {
		t.Fatal("executed host without its packaged provisioning profile")
	}
}

// Hosted runners report a refused probe as a warning; a missing profile stays an error.
func TestAdvisoryCredentialProbeWarnsOnlyForTheKeychainCall(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	runner := signedOK()
	runner.err[filepath.Join(app, sidecarRelPath)+" credentials"] = errors.New("OSStatus -26275")
	findings := checkCredentialProtection(context.Background(), runner, Options{AppPath: app, RequireSigned: true, CredentialProbeAdvisory: true})
	if len(findings) != 1 || findings[0].Code != CodeCredentialProtectionFailed || findings[0].Severity != SeverityWarn {
		t.Fatalf("advisory probe findings: %+v", findings)
	}

	profile := filepath.Join(app, filepath.Dir(filepath.Dir(sidecarRelPath)), "embedded.provisionprofile")
	if err := os.Remove(profile); err != nil {
		t.Fatalf("remove profile fixture: %v", err)
	}
	findings = checkCredentialProtection(context.Background(), signedOK(), Options{AppPath: app, RequireSigned: true, CredentialProbeAdvisory: true})
	if len(findings) != 1 || findings[0].Severity != SeverityError {
		t.Fatalf("missing profile must stay an error: %+v", findings)
	}
}
