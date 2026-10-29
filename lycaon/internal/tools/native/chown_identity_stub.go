//go:build !unix

package native

import "github.com/lycaon/lycaon/internal/toolrejection"

func chownSupported() bool { return false }

func resolveChownIdentities(ownerSpec, groupSpec string) (uid, gid int, err error) {
	return 0, 0, &toolrejection.ToolReject{Code: "CHOWN_UNSUPPORTED", Data: nil}
}

func fileOwnership(path string) (uid, gid int, err error) {
	return 0, 0, &toolrejection.ToolReject{Code: "CHOWN_UNSUPPORTED", Data: nil}
}
