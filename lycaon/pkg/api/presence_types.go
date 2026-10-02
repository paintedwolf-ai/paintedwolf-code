package api

// PresenceAuthenticator names the operating-system user-presence verifier
// that confirmed the person before the desktop shell signed.
type PresenceAuthenticator string

const (
	PresenceAuthenticatorMacOS   PresenceAuthenticator = "macos_user_presence"
	PresenceAuthenticatorWindows PresenceAuthenticator = "windows_user_presence"
)
