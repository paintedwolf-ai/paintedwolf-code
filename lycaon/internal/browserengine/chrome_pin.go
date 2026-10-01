package browserengine

// PinnedChromeHeadlessShell selects the managed browser version on every platform.
const PinnedChromeHeadlessShell = "154.0.8037.92"

// pinnedChromeHeadlessShellSHA256 maps artifact IDs to reviewed archive hashes.
var pinnedChromeHeadlessShellSHA256 = map[string]string{
	"mac-arm64":   "77da14e75d7f2568e6f7898d3df7cdc6faac74b15e903b2c9d486ebb6ca9b929",
	"mac-x64":     "a54292aaacbb77f76f6ef47558e7c51ab884044e0adacca315567f83c060bcc4",
	"linux64":     "636aa5c79f2693632e9921b8bbb050038ba11672e02346c06c20f991aed096f9",
	"linux-arm64": "0ed0e47d9e9f639197f508d62ada09e5c6b4c4c60edab3160a9312a733091df6",
	"win64":       "3ac2561f02d9d87aadc0399d00b9002d718a4c365624fa67db9e7bfaf6b1a568",
	"win32":       "56b30d2d6c35775ebf8dc3618680f6529e1c38c87f7feb28a16e9904273d51f7",
}

// chromeForTestingBase is the CfT storage origin (overridable in tests).
var chromeForTestingBase = "https://storage.googleapis.com/chrome-for-testing-public"
