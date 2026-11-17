package launchverify

// VerifyExecutable hashes the image pid is running, through the exe link
// opened as a descriptor, and compares it with the manifest's entry for the
// role's installed path.
func (v Verifier) VerifyExecutable(pid int, role string) error {
	return v.verifyExecutableAgainst(pid, role, v.Manifest, nil)
}
