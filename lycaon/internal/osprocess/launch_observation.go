package osprocess

// ProcessSecurity reports kernel credentials, not a host permission or person.
type ProcessSecurity struct {
	RealUID, EffectiveUID, SavedUID uint32
	RealGID, EffectiveGID, SavedGID uint32
	Linux                           *LinuxPrivileges
}

// LinuxPrivileges distinguishes observed capability sets and no-new-privileges.
type LinuxPrivileges struct {
	Permitted, Effective, Ambient uint64
	Inheritable, Bounding         uint64
	NoNewPrivileges               bool
}

// LaunchObservation is a stopped process snapshot consumed by the launch guard.
// Image, cwd, arguments/environment and credentials remain separate facts.
type LaunchObservation struct {
	ExecutableMatches bool
	DirectoryMatches  bool
	Arguments         []string
	Environment       []string
	// EnvironmentObserved distinguishes returned environment bytes from native
	// omission. Darwin can hide even an owned restricted process environment;
	// a zero-length result there is conservatively unknown, never known empty.
	EnvironmentObserved bool
	Security            ProcessSecurity
}
