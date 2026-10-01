// Package version holds the application version and bundle identifier.
// Release builds inject VERSION through linker flags.
package version

// Version is the host product version (no "v" prefix). Overridden by ldflags.
var Version = "dev"

// BundleID identifies the desktop app and its vault identity.
const BundleID = "dev.paintedwolf.code"
