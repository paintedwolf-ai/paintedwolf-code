package extpacks

// AcquireCacheMutationLock serializes package-cache writes across processes.
func AcquireCacheMutationLock() (func(), error) {
	root, err := CacheRoot()
	if err != nil {
		return nil, err
	}
	unlock := lockExtensionPath(root)
	releaseFile, err := lockExtensionPathAcrossProcesses(root)
	if err != nil {
		unlock()
		return nil, err
	}
	return func() {
		releaseFile()
		unlock()
	}, nil
}
