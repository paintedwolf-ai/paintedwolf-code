//go:build darwin

package layeredfs

const (
	platformPrimaryTestXattr   = "com.apple.quarantine"
	platformSecondaryTestXattr = "user.layeredfs.custom"
)
