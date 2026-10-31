package native

import (
	"errors"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// chownAccounts is the identity adapter chown resolves owner and group
// specifications and current ownership against.
type chownAccounts interface {
	Capabilities() hostos.Capabilities
	CurrentIDs() (uid, gid int, err error)
	LookupUser(name string) (int, error)
	LookupGroup(name string) (int, error)
	FileOwner(path string) (uid, gid int, err error)
}

func chownSupported() bool { return hostos.Host{}.Capabilities().NumericOwnership }

func fileOwnership(path string) (uid, gid int, err error) {
	return ownershipOf(hostos.Host{}, path)
}

func ownershipOf(accounts chownAccounts, path string) (uid, gid int, err error) {
	uid, gid, err = accounts.FileOwner(path)
	if errors.Is(err, hostos.ErrUnsupported) {
		return 0, 0, chownUnsupported()
	}
	return uid, gid, err
}

func chownUnsupported() error { return &toolrejection.ToolReject{Code: "CHOWN_UNSUPPORTED", Data: nil} }

// resolveChownIdentities admits only the host process's own user and group,
// by name, number or "current"; root is refused to a non-root process.
func resolveChownIdentities(accounts chownAccounts, ownerSpec, groupSpec string) (uid, gid int, err error) {
	if !accounts.Capabilities().NumericOwnership {
		return 0, 0, chownUnsupported()
	}
	currentUID, currentGID, err := accounts.CurrentIDs()
	if errors.Is(err, hostos.ErrUnsupported) {
		return 0, 0, chownUnsupported()
	}
	if err != nil {
		return 0, 0, err
	}
	policy := chownPolicy{accounts: accounts, uid: currentUID, gid: currentGID}
	if uid, err = policy.owner(ownerSpec); err != nil {
		return 0, 0, err
	}
	if gid, err = policy.group(groupSpec); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

type chownPolicy struct {
	accounts chownAccounts
	uid, gid int
}

func (p chownPolicy) owner(spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "current") {
		return p.uid, nil
	}
	uid, err := p.resolve("owner", spec, p.accounts.LookupUser, "CHOWN_USER_UNKNOWN")
	if err != nil {
		return 0, err
	}
	if uid == 0 && p.uid != 0 {
		return 0, &toolrejection.ToolReject{Code: "CHOWN_ROOT_DENIED", Data: p.data("owner", spec)}
	}
	if uid != p.uid {
		return 0, p.denied("owner", spec)
	}
	return uid, nil
}

func (p chownPolicy) group(spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "current") {
		return p.gid, nil
	}
	gid, err := p.resolve("group", spec, p.accounts.LookupGroup, "CHOWN_GROUP_UNKNOWN")
	if err != nil {
		return 0, err
	}
	if gid != p.gid {
		return 0, p.denied("group", spec)
	}
	return gid, nil
}

// resolve reads a numeric id directly and looks a name up in the adapter.
func (p chownPolicy) resolve(field, spec string, lookup func(string) (int, error), unknownCode string) (int, error) {
	if isNumericIDSpec(spec) {
		id, err := strconv.Atoi(spec)
		if err != nil {
			return 0, p.denied(field, spec)
		}
		return id, nil
	}
	id, err := lookup(spec)
	switch {
	case errors.Is(err, hostos.ErrUnsupported):
		return 0, chownUnsupported()
	case errors.Is(err, hostos.ErrUnknownAccount):
		return 0, &toolrejection.ToolReject{Code: unknownCode, Data: p.data(field, spec)}
	case err != nil:
		return 0, p.denied(field, spec)
	}
	return id, nil
}

func (p chownPolicy) denied(field, value string) error {
	return &toolrejection.ToolReject{Code: "CHOWN_TARGET_DENIED", Data: p.data(field, value)}
}

func (p chownPolicy) data(field, value string) map[string]any {
	return map[string]any{
		field: value, "ownership_field": field, "ownership_target": value,
		"ownership_uid": p.uid, "ownership_gid": p.gid,
	}
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
