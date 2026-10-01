package confine

// packageCacheHomeRelRoots are home-relative package stores allowed as default
// write roots. Each names the store, not the tool home, because install and bin
// directories execute outside the sandbox.
var packageCacheHomeRelRoots = []string{
	".bun/install/cache",
	".npm/_cacache",
	".cargo/registry",
	".cargo/git",
}
