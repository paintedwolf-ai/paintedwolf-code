package bundleverify

import (
	"maps"
	"testing"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Signature fixtures retain the stderr fields consumed by the parser.

const codesignAdhoc = `Executable=/tmp/Painted Wolf Code.app/Contents/MacOS/pw
Identifier=dev.paintedwolf.code
Format=Mach-O thin (arm64)
CodeDirectory v=20400 size=1234 flags=0x2(adhoc) hashes=30+7 location=embedded
Signature=adhoc
Info.plist=not bound
TeamIdentifier=not set
`

const codesignDeveloperIDNoRuntime = `Executable=/tmp/Painted Wolf Code.app/Contents/MacOS/pw
Identifier=dev.paintedwolf.code
Format=Mach-O thin (arm64)
CodeDirectory v=20400 size=1234 flags=0x0(none) hashes=30+7 location=embedded
Signature size=8973
Authority=Developer ID Application: Example LLC (ABCDE12345)
TeamIdentifier=ABCDE12345
`

const codesignDeveloperIDWithRuntime = `Executable=/tmp/Painted Wolf Code.app/Contents/MacOS/pw
Identifier=dev.paintedwolf.code
Format=Mach-O thin (arm64)
CodeDirectory v=20500 size=1234 flags=0x10000(runtime) hashes=30+7 location=embedded
Signature size=8973
Authority=Developer ID Application: Example LLC (ABCDE12345)
TeamIdentifier=ABCDE12345
`

// Entitlement fixtures model the command's stdout formats.

const entitlementsBrowserOK = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>com.apple.security.cs.allow-jit</key><true/><key>com.apple.security.cs.allow-unsigned-executable-memory</key><true/></dict></plist>`

const entitlementsEmpty = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict/></plist>`

const entitlementsEngineOK = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.application-identifier</key>
	<string>ABCDE12345.dev.paintedwolf.code.engine</string>
	<key>com.apple.developer.team-identifier</key>
	<string>ABCDE12345</string>
	<key>keychain-access-groups</key>
	<array>
		<string>ABCDE12345.dev.paintedwolf.code.engine</string>
	</array>
</dict>
</plist>`

func TestParseEntitlements(t *testing.T) {
	cases := []struct {
		name string
		data string
		want map[string]bool
	}{
		{"browser grants", entitlementsBrowserOK, map[string]bool{
			"com.apple.security.cs.allow-jit": true, "com.apple.security.cs.allow-unsigned-executable-memory": true}},
		{"profile values that are not booleans", entitlementsEngineOK, map[string]bool{
			"com.apple.application-identifier": false, "com.apple.developer.team-identifier": false, "keychain-access-groups": false}},
		{"explicit false", "<plist><dict><key>com.apple.security.cs.allow-jit</key><false/></dict></plist>",
			map[string]bool{"com.apple.security.cs.allow-jit": false}},
		{"empty dictionary", entitlementsEmpty, map[string]bool{}},
		{"no entitlements blob", "", map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEntitlements([]byte(tc.data))
			testutil.FailErr(t, "parse entitlements", err)
			if !maps.Equal(got, tc.want) {
				t.Fatalf("parseEntitlements = %v, want %v", got, tc.want)
			}
		})
	}
	if _, err := parseEntitlements([]byte("<plist><dict><key>truncated")); err == nil {
		t.Fatal("parsed a truncated entitlements plist")
	}
}

func TestParseCodesignFlags(t *testing.T) {
	cases := []struct {
		name      string
		output    string
		wantFlags uint32
		wantOK    bool
	}{
		{"adhoc", codesignAdhoc, 0x2, true},
		{"developer id without hardened runtime", codesignDeveloperIDNoRuntime, 0x0, true},
		{"developer id with hardened runtime", codesignDeveloperIDWithRuntime, 0x10000, true},
		{"no flags token", "Executable=/tmp/x\nIdentifier=y\n", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, ok := parseCodesignFlags([]byte(tc.output))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if flags != tc.wantFlags {
				t.Fatalf("flags = %#x, want %#x", flags, tc.wantFlags)
			}
		})
	}
}

func TestHardenedRuntimeBit(t *testing.T) {
	flags, ok := parseCodesignFlags([]byte(codesignDeveloperIDWithRuntime))
	if !ok {
		t.Fatal("expected to parse flags")
	}
	if flags&hardenedRuntimeFlag == 0 {
		t.Fatalf("flags %#x should carry the hardened-runtime bit %#x", flags, hardenedRuntimeFlag)
	}

	flags, ok = parseCodesignFlags([]byte(codesignDeveloperIDNoRuntime))
	if !ok {
		t.Fatal("expected to parse flags")
	}
	if flags&hardenedRuntimeFlag != 0 {
		t.Fatalf("flags %#x should not carry the hardened-runtime bit", flags)
	}
}

func TestIsAdhocSignature(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"adhoc", codesignAdhoc, true},
		{"developer id", codesignDeveloperIDWithRuntime, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, ok := parseCodesignFlags([]byte(tc.output))
			if got := isAdhocSignature([]byte(tc.output), flags, ok); got != tc.want {
				t.Fatalf("isAdhocSignature = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompareVersionIsNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"13.0", "13.0", 0},
		{"9.0", "13.0", -1}, // the lexical-compare trap
		{"13.0", "9.0", 1},  // and its mirror
		{"15.0", "13.0", 1},
		{"13.1", "13.0", 1},
		{"13", "13.0", 0},
	}

	for _, tc := range cases {
		got, err := dotversion.Compare(tc.a, tc.b)
		if err != nil {
			t.Fatalf("dotversion.Compare(%q, %q): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Fatalf("dotversion.Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareVersionRejectsGarbage(t *testing.T) {
	if _, err := dotversion.Compare("thirteen", "13.0"); err == nil {
		t.Fatal("expected an error for an unparseable version")
	}
}
