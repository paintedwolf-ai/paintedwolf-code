package linuxsandbox

import (
	"errors"
	"testing"
)

func TestParseUnavailableReadsTheMarkerLine(t *testing.T) {
	t.Parallel()
	want := Unavailability{Code: UnavailableUserNamespacesAppArmor, Remedy: RemedyInstallPackage, Profile: AppArmorProfile}
	stderr := "noise\nconfine: " + UnavailableMarker + want.UnavailableJSON() + "\n"
	got, ok := ParseUnavailable(stderr)
	if !ok || got != want {
		t.Fatalf("ParseUnavailable = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := ParseUnavailable("confine: exec denied\n"); ok {
		t.Fatal("plain failure parsed as unavailability")
	}
	if _, ok := ParseUnavailable("confine: " + UnavailableMarker + "{}\n"); ok {
		t.Fatal("reason without a code parsed as unavailability")
	}
}

func TestRecordRunClassifiesTheHelperOutcome(t *testing.T) {
	t.Parallel()
	var applied Capabilities
	applied.RecordRun(nil, "")
	if !applied.Applied || !applied.UserNamespaces || !applied.Available() {
		t.Fatalf("clean run: %+v", applied)
	}

	var typed Capabilities
	reason := Unavailability{Code: UnavailableLandlockABI, Detail: "ABI 1", Remedy: RemedyUpgradeKernel}
	typed.RecordRun(errors.New("exit status 125"), "confine: "+UnavailableMarker+reason.UnavailableJSON()+"\n")
	if typed.Available() || len(typed.Unavailable) != 1 || typed.Unavailable[0] != reason {
		t.Fatalf("typed refusal: %+v", typed)
	}

	var crashed Capabilities
	crashed.RecordRun(errors.New("exit status 126"), "confine: attestation failed\n")
	if crashed.Available() || len(crashed.Unavailable) != 1 || crashed.Unavailable[0].Code != UnavailableHelper ||
		crashed.Unavailable[0].Detail != "confine: attestation failed" {
		t.Fatalf("untyped failure: %+v", crashed)
	}

	var silent Capabilities
	silent.RecordRun(errors.New("signal: killed"), "")
	if silent.Unavailable[0].Detail != "signal: killed" {
		t.Fatalf("silent failure keeps the error: %+v", silent)
	}
}

func TestUnavailabilityIsAnError(t *testing.T) {
	t.Parallel()
	err := error(&Unavailability{Code: UnavailableSeccomp, Detail: "ENOSYS"})
	u, ok := AsUnavailable(err)
	if !ok || u.Code != UnavailableSeccomp || err.Error() != "linux sandbox unavailable: seccomp_unavailable: ENOSYS" {
		t.Fatalf("AsUnavailable = %+v, %v (%v)", u, ok, err)
	}
	if _, ok := AsUnavailable(errors.New("other")); ok {
		t.Fatal("plain error classified as unavailability")
	}
	unsupported := error(&Unsupported{Code: UnsupportedNetworkMode, Detail: "tunnel"})
	if unsupported.Error() != "linux sandbox cannot express this boundary: network_mode: tunnel" {
		t.Fatalf("Unsupported message: %q", unsupported.Error())
	}
}
