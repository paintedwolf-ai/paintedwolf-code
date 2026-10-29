package native

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// fixtureAccounts is an identity adapter fact set: a platform that supports
// numeric ownership, one whose name lookups are unavailable, or one without
// numeric ownership at all.
type fixtureAccounts struct {
	capabilities hostos.Capabilities
	uid, gid     int
	currentErr   error
	users        map[string]int
	groups       map[string]int
	lookupErr    error
	owners       map[string][2]int
}

func (f fixtureAccounts) Capabilities() hostos.Capabilities { return f.capabilities }
func (f fixtureAccounts) CurrentIDs() (int, int, error)     { return f.uid, f.gid, f.currentErr }
func (f fixtureAccounts) LookupUser(name string) (int, error) {
	return f.lookup(f.users, name)
}
func (f fixtureAccounts) LookupGroup(name string) (int, error) {
	return f.lookup(f.groups, name)
}
func (f fixtureAccounts) FileOwner(path string) (int, int, error) {
	if !f.capabilities.NumericOwnership {
		return 0, 0, hostos.ErrUnsupported
	}
	owner, ok := f.owners[path]
	if !ok {
		return 0, 0, fmt.Errorf("lstat %s: no such file", path)
	}
	return owner[0], owner[1], nil
}

func (f fixtureAccounts) lookup(table map[string]int, name string) (int, error) {
	if f.lookupErr != nil {
		return 0, f.lookupErr
	}
	id, ok := table[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s", hostos.ErrUnknownAccount, name)
	}
	return id, nil
}

func supportedAccounts() fixtureAccounts {
	return fixtureAccounts{
		capabilities: hostos.Capabilities{NumericOwnership: true},
		uid:          501, gid: 20,
		users:  map[string]int{"ann": 501, "root": 0, "bob": 502},
		groups: map[string]int{"staff": 20, "wheel": 0},
		owners: map[string][2]int{"/project/file": {501, 20}},
	}
}

func TestChownIdentityResolvesAgainstAccountFacts(t *testing.T) {
	supported := supportedAccounts()
	namesUnavailable := supportedAccounts()
	namesUnavailable.lookupErr = hostos.ErrUnsupported
	lookupFailed := supportedAccounts()
	lookupFailed.lookupErr = errors.New("account id is not numeric")
	rootProcess := supportedAccounts()
	rootProcess.uid, rootProcess.gid = 0, 0
	unsupported := fixtureAccounts{}
	idsUnavailable := supportedAccounts()
	idsUnavailable.currentErr = hostos.ErrUnsupported
	idsFailed := supportedAccounts()
	idsFailed.currentErr = errors.New("identity read failed")

	cases := []struct {
		name          string
		accounts      fixtureAccounts
		owner, group  string
		uid, gid      int
		code, rawFail string
	}{
		{name: "current", accounts: supported, owner: "current", group: "", uid: 501, gid: 20},
		{name: "numeric", accounts: supported, owner: " 501 ", group: "20", uid: 501, gid: 20},
		{name: "names", accounts: supported, owner: "ann", group: "staff", uid: 501, gid: 20},
		{name: "foreign numeric owner", accounts: supported, owner: "502", code: "CHOWN_TARGET_DENIED"},
		{name: "foreign named owner", accounts: supported, owner: "bob", code: "CHOWN_TARGET_DENIED"},
		{name: "oversized id", accounts: supported, owner: "99999999999999999999999", code: "CHOWN_TARGET_DENIED"},
		{name: "root by number", accounts: supported, owner: "0", code: "CHOWN_ROOT_DENIED"},
		{name: "root by name", accounts: supported, owner: "root", code: "CHOWN_ROOT_DENIED"},
		{name: "root process", accounts: rootProcess, owner: "root", group: "wheel", uid: 0, gid: 0},
		{name: "unknown user", accounts: supported, owner: "nobody-here", code: "CHOWN_USER_UNKNOWN"},
		{name: "unknown group", accounts: supported, owner: "current", group: "nobody-here", code: "CHOWN_GROUP_UNKNOWN"},
		{name: "foreign group", accounts: supported, owner: "current", group: "wheel", code: "CHOWN_TARGET_DENIED"},
		{name: "foreign numeric group", accounts: supported, owner: "current", group: "0", code: "CHOWN_TARGET_DENIED"},
		{name: "malformed account", accounts: lookupFailed, owner: "ann", code: "CHOWN_TARGET_DENIED"},
		{name: "names unavailable", accounts: namesUnavailable, owner: "ann", code: "CHOWN_UNSUPPORTED"},
		{name: "numbers without name lookups", accounts: namesUnavailable, owner: "501", group: "current", uid: 501, gid: 20},
		{name: "ids unavailable", accounts: idsUnavailable, owner: "current", code: "CHOWN_UNSUPPORTED"},
		{name: "ids failed", accounts: idsFailed, owner: "current", rawFail: "identity read failed"},
		{name: "unsupported", accounts: unsupported, owner: "current", code: "CHOWN_UNSUPPORTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid, gid, err := resolveChownIdentities(tc.accounts, tc.owner, tc.group)
			switch {
			case tc.rawFail != "":
				if err == nil || err.Error() != tc.rawFail {
					t.Fatalf("err = %v, want %s", err, tc.rawFail)
				}
			case tc.code != "":
				var reject *toolrejection.ToolReject
				if !errors.As(err, &reject) || reject.Code != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
				if tc.code != "CHOWN_UNSUPPORTED" && (reject.Data["ownership_uid"] != tc.accounts.uid || reject.Data["ownership_gid"] != tc.accounts.gid) {
					t.Fatalf("refusal data %+v", reject.Data)
				}
			default:
				if err != nil || uid != tc.uid || gid != tc.gid {
					t.Fatalf("resolved %d:%d (%v), want %d:%d", uid, gid, err, tc.uid, tc.gid)
				}
			}
		})
	}
}

func TestChownOwnershipReadsAccountFacts(t *testing.T) {
	if uid, gid, err := ownershipOf(supportedAccounts(), "/project/file"); err != nil || uid != 501 || gid != 20 {
		t.Fatalf("owner %d:%d (%v)", uid, gid, err)
	}
	if _, _, err := ownershipOf(supportedAccounts(), "/project/missing"); err == nil {
		t.Fatal("missing file has an owner")
	}
	var reject *toolrejection.ToolReject
	if _, _, err := ownershipOf(fixtureAccounts{}, "/project/file"); !errors.As(err, &reject) || reject.Code != "CHOWN_UNSUPPORTED" {
		t.Fatalf("unsupported ownership: %v", err)
	}
}
