package confine

// PrivateTempFor describes the Linux mount view for a confined launch.
func PrivateTempFor(scratch string) PrivateTemp {
	return PrivateTemp{Paths: []string{"/tmp", "/var/tmp"}, ScratchRoot: scratch}
}
