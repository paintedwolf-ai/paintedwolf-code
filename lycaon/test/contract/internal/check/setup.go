package check

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
)

func Setup() {
	gittestsetup.Enable()

	// Install the shipped confinement catalogs.
	keyPaths, err := detectionpack.BundledKeyMaterialPaths()
	if err != nil {
		panic(err)
	}
	confine.SetKeyMaterialPathsSource(func() []string { return keyPaths })
	credentialPaths, err := detectionpack.BundledCredentialStorePaths()
	if err != nil {
		panic(err)
	}
	confine.SetCredentialStorePathsSource(func() []string { return credentialPaths })
	anchortestsetup.Install()
}
