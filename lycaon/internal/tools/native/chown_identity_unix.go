//go:build unix

package native

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/tools"
)

func chownSupported() bool { return true }

func resolveChownIdentities(ownerSpec, groupSpec string) (uid, gid int, err error) {
	uid, err = resolveChownUID(ownerSpec)
	if err != nil {
		return 0, 0, err
	}
	gid, err = resolveChownGID(groupSpec)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func resolveChownUID(spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "current") {
		return os.Getuid(), nil
	}
	if isNumericIDSpec(spec) {
		uid, convErr := strconv.Atoi(spec)
		if convErr != nil || uid < 0 {
			return 0, chownTargetDenied("owner", spec)
		}
		if uid == 0 && os.Getuid() != 0 {
			return 0, &tools.ToolReject{
				Code: "CHOWN_ROOT_DENIED",
				Data: chownIdentityData("owner", spec),
			}
		}
		if uid != os.Getuid() {
			return 0, chownTargetDenied("owner", spec)
		}
		return uid, nil
	}
	u, lookupErr := user.Lookup(spec)
	if lookupErr != nil {
		return 0, &tools.ToolReject{
			Code: "CHOWN_USER_UNKNOWN",
			Data: chownIdentityData("owner", spec),
		}
	}
	uid, convErr := strconv.Atoi(u.Uid)
	if convErr != nil {
		return 0, chownTargetDenied("owner", spec)
	}
	if uid == 0 && os.Getuid() != 0 {
		return 0, &tools.ToolReject{
			Code: "CHOWN_ROOT_DENIED",
			Data: chownIdentityData("owner", spec),
		}
	}
	if uid != os.Getuid() {
		return 0, chownTargetDenied("owner", spec)
	}
	return uid, nil
}

func resolveChownGID(spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "current") {
		return os.Getgid(), nil
	}
	if isNumericIDSpec(spec) {
		gid, convErr := strconv.Atoi(spec)
		if convErr != nil || gid < 0 {
			return 0, chownTargetDenied("group", spec)
		}
		if gid != os.Getgid() {
			return 0, chownTargetDenied("group", spec)
		}
		return gid, nil
	}
	g, lookupErr := user.LookupGroup(spec)
	if lookupErr != nil {
		return 0, &tools.ToolReject{
			Code: "CHOWN_GROUP_UNKNOWN",
			Data: chownIdentityData("group", spec),
		}
	}
	gid, convErr := strconv.Atoi(g.Gid)
	if convErr != nil {
		return 0, chownTargetDenied("group", spec)
	}
	if gid != os.Getgid() {
		return 0, chownTargetDenied("group", spec)
	}
	return gid, nil
}

func isNumericIDSpec(spec string) bool {
	if spec == "" {
		return false
	}
	for _, r := range spec {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func chownTargetDenied(field, value string) error {
	return &tools.ToolReject{
		Code: "CHOWN_TARGET_DENIED",
		Data: chownIdentityData(field, value),
	}
}

func fileOwnership(path string) (uid, gid int, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("ownership metadata unavailable for %s", path)
	}
	return int(st.Uid), int(st.Gid), nil
}

func chownIdentityData(field, value string) map[string]any {
	return map[string]any{
		field: value, "ownership_field": field, "ownership_target": value,
		"ownership_uid": os.Getuid(), "ownership_gid": os.Getgid(),
	}
}
