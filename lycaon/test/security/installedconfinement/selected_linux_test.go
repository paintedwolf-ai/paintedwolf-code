//go:build linux && installedhelper

package installedconfinement

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

type selectedHelper struct {
	path, digest, outcome, refusal string
}

func requireSelectedHelper(t *testing.T) selectedHelper {
	t.Helper()
	helper := selectedHelper{os.Getenv("PW_INSTALLED_CONFINEMENT_HELPER"), os.Getenv("PW_INSTALLED_CONFINEMENT_SHA256"), os.Getenv("PW_INSTALLED_CONFINEMENT_OUTCOME"), os.Getenv("PW_INSTALLED_CONFINEMENT_REFUSAL")}
	if !filepath.IsAbs(helper.path) || (helper.outcome != "applied" && helper.outcome != "refused") {
		t.Fatal("installed helper requires an absolute selected native file and explicit applied/refused outcome")
	}
	decoded, err := hex.DecodeString(helper.digest)
	if err != nil || len(decoded) != sha256.Size || helper.digest != strings.ToLower(helper.digest) {
		t.Fatal("installed helper requires its selected final SHA-256")
	}
	if helper.outcome == "refused" && helper.refusal == "" {
		t.Fatal("refused installed helper requires an expected structured refusal code")
	}
	helper.path, err = filepath.EvalSymlinks(helper.path)
	testutil.FailErr(t, "resolve explicitly selected installed helper", err)
	// A root-owned sticky store protects the already checked root-owned child.
	for path := helper.path; ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		testutil.FailErr(t, "inspect installed helper ownership chain", err)
		owner, ok := info.Sys().(*syscall.Stat_t)
		mutable := info.Mode().Perm()&0o022 != 0 && (!info.IsDir() || info.Mode()&os.ModeSticky == 0)
		if !ok || owner.Uid != 0 || mutable {
			t.Fatalf("selected installed helper has a mutable or non-root-owned component: %s", path)
		}
		if path == "/" {
			break
		}
	}
	info, err := os.Stat(helper.path)
	testutil.FailErr(t, "inspect selected native helper", err)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatal("selected installed helper is not an ordinary native executable")
	}
	capabilities, err := unix.Getxattr(helper.path, "security.capability", nil)
	if capabilities != 0 || err != nil && err != unix.ENODATA && err != unix.ENOTSUP {
		t.Fatalf("selected helper has file capabilities or uninspectable privilege metadata: size=%d error=%v", capabilities, err)
	}
	native, err := elf.Open(helper.path)
	testutil.FailErr(t, "read installed helper ELF identity", err)
	defer func() { _ = native.Close() }()
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if want == elf.EM_NONE || native.Machine != want {
		t.Fatalf("selected helper is not native to this qualification host: %s", native.Machine)
	}
	helper.verify(t)
	return helper
}

func (helper selectedHelper) verify(t *testing.T) {
	t.Helper()
	file, err := os.Open(helper.path)
	testutil.FailErr(t, "open final installed helper bytes", err)
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	_, err = io.Copy(digest, file)
	testutil.FailErr(t, "hash final installed helper bytes", err)
	if got := hex.EncodeToString(digest.Sum(nil)); got != helper.digest {
		t.Fatalf("installed helper differs from selected final bytes: got %s", got)
	}
}
