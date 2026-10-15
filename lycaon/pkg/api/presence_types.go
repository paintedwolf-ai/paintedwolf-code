package api

// PresenceAuthenticator names the user-presence verifier that confirmed the
// person before the desktop shell signed.
type PresenceAuthenticator string

const (
	PresenceAuthenticatorMacOS   PresenceAuthenticator = "macos_user_presence"
	PresenceAuthenticatorWindows PresenceAuthenticator = "windows_user_presence"
	PresenceAuthenticatorLinux   PresenceAuthenticator = "linux_user_presence"
	// PresenceAuthenticatorLinuxAppPassword: the shell's native dialog
	// re-verified the app password because no polkit agent could confirm.
	PresenceAuthenticatorLinuxAppPassword PresenceAuthenticator = "linux_app_password" // #nosec G101 -- an authenticator name, not a credential
)
