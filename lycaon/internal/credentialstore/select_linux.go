package credentialstore

import "sync"

// linuxCustody is decided once per process: the identity service either
// verified this engine or it did not, and that fact also decides whether
// the vault opens unattended.
var linuxCustody = sync.OnceValue(func() custody { return probeCustody(newServiceClient()) })

// unattendedIdentity reports verified service custody.
func unattendedIdentity() bool { return linuxCustody().mode == custodyService }

// verifiedLauncherPID is the desktop app the identity service verified as
// this engine's parent, or 0.
func verifiedLauncherPID() int { return linuxCustody().launcherPID }

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return linuxCustody().provider(path)
}
