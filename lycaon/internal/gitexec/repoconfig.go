package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

const repoConfigAuditTimeout = 10 * time.Second

// UnsafeRepoConfigError reports executable repository configuration.
type UnsafeRepoConfigError struct {
	Keys []string
}

func (e *UnsafeRepoConfigError) Error() string {
	return "gitexec: repository config declares commands this host will not run: " + strings.Join(e.Keys, ", ")
}

// Code returns the structured reject code for tool/API mapping.
func (e *UnsafeRepoConfigError) Code() string { return "GIT_REPO_CONFIG_UNSAFE" }

// KeyList renders the offending keys for notice copy.
func (e *UnsafeRepoConfigError) KeyList() string { return strings.Join(e.Keys, ", ") }

// lfsFilterName identifies the host-defined filter.
const lfsFilterName = "lfs"

// refusedFixedKeys have no inert value or disabling flag.
var refusedFixedKeys = []string{"core.gitProxy"}

// executableSubsectionFamily describes an open-named command family.
type executableSubsectionFamily struct {
	Section     string
	Variables   []string
	HostDefined string
}

// Only families without a disabling flag belong here.
var executableSubsectionFamilies = []executableSubsectionFamily{
	{
		// Filters run during staging and checkout.
		Section:     "filter",
		Variables:   []string{"clean", "smudge", "process"},
		HostDefined: lfsFilterName,
	},
	{
		// Merge drivers have no disabling flag.
		Section:   "merge",
		Variables: []string{"driver"},
	},
}

// auditRepoConfig rejects executable settings from the discovered repository.
func auditRepoConfig(ctx context.Context, dir string) error {
	repo, inRepo := gitrepo.Discover(dir)
	if !inRepo {
		return nil
	}
	cacheKey := repoAuditCacheKey(repo)
	if cacheKey != "" {
		if cached, hit := repoAuditCache.Load(cacheKey); hit {
			return unsafeRepoConfigResult(cached)
		}
	}
	keys, err := unsafeRepoConfigKeys(ctx, dir)
	if err != nil {
		// Failed audits are not cached.
		return err
	}
	if cacheKey != "" {
		repoAuditCache.Store(cacheKey, keys)
	}
	return unsafeRepoConfigResult(keys)
}

var repoAuditCache sync.Map // string -> []string

func unsafeRepoConfigResult(v any) error {
	keys, _ := v.([]string)
	if len(keys) == 0 {
		return nil
	}
	return &UnsafeRepoConfigError{Keys: keys}
}

// Content and absence are part of the audit cache identity.
func repoAuditCacheKey(repo gitrepo.Repo) string {
	files := repo.LocalConfigFiles()
	if len(files) == 0 {
		// Unresolved layouts are audited on every call.
		return ""
	}
	sum := sha256.New()
	sum.Write([]byte(repo.Root))
	for _, path := range files {
		sum.Write([]byte{0})
		sum.Write([]byte(path))
		sum.Write([]byte{0})
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			sum.Write([]byte(strconv.Itoa(len(raw))))
			sum.Write([]byte{0})
			sum.Write(raw)
		case errors.Is(err, fs.ErrNotExist):
			sum.Write([]byte("absent"))
		default:
			return "" // unreadable input: no honest key
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// unsafeRepoConfigKeys reads repository-local key names.
func unsafeRepoConfigKeys(ctx context.Context, dir string) ([]string, error) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, err
	}
	auditCtx, cancel := context.WithTimeout(ctx, repoConfigAuditTimeout)
	defer cancel()

	out, code, err := exec.Run(auditCtx, bin, repoConfigAuditArgv(), exec.ExecOpts{
		Launch:         exec.HostLaunch("git_repo_config_audit"),
		Dir:            dir,
		Timeout:        repoConfigAuditTimeout,
		MaxOutputBytes: 4 << 20,
		AppendEnv: append([]string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_PAGER=cat",
			"GIT_EXEC_PATH=" + gitExecPath(bin),
		}, bundledRuntimeEnv(bin)...),
	})
	if err != nil {
		return nil, err
	}
	// Exit 1 with no output means no local configuration.
	if code != 0 && len(strings.TrimSpace(string(out))) > 0 {
		return nil, fmt.Errorf("gitexec: read repository config: exit %d", code)
	}
	return classifyRepoConfigKeys(out), nil
}

// repoConfigAuditArgv constructs the local configuration query.
func repoConfigAuditArgv() []string {
	return []string{"config", "--local", "--list", "--name-only", "--null"}
}

func classifyRepoConfigKeys(out []byte) []string {
	found := map[string]bool{}
	for _, raw := range strings.Split(string(out), "\x00") {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if fixed, ok := refusedFixedKey(key); ok {
			found[fixed] = true
			continue
		}
		if name, family, ok := executableSubsectionName(key); ok {
			found[family.Section+"."+name] = true
		}
	}
	if len(found) == 0 {
		return nil
	}
	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// refusedFixedKey returns the canonical spelling of a refused key.
func refusedFixedKey(key string) (string, bool) {
	for _, want := range refusedFixedKeys {
		if strings.EqualFold(key, want) {
			return want, true
		}
	}
	return "", false
}

// Subsections are case-sensitive; sections and variables are not.
func executableSubsectionName(key string) (string, executableSubsectionFamily, bool) {
	for _, family := range executableSubsectionFamilies {
		prefix := family.Section + "."
		if len(key) <= len(prefix) || !strings.EqualFold(key[:len(prefix)], prefix) {
			continue
		}
		rest := key[len(prefix):]
		dot := strings.LastIndex(rest, ".")
		if dot <= 0 {
			continue
		}
		if !containsFold(family.Variables, rest[dot+1:]) {
			continue
		}
		name := rest[:dot]
		if name == "" || (family.HostDefined != "" && name == family.HostDefined) {
			continue
		}
		return name, family, true
	}
	return "", executableSubsectionFamily{}, false
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
