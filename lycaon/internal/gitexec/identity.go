package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

const identityQueryTimeout = 5 * time.Second

type cachedIdentity struct {
	id Identity
	ok bool
}

var identityCache sync.Map // string -> cachedIdentity

// ResolveIdentity returns a complete repository-over-global identity.
func ResolveIdentity(ctx context.Context, repoDir string) (Identity, bool) {
	bin, err := binaryPathFn()
	if err != nil {
		return Identity{}, false
	}
	prefix, err := neutralizeArgs(resolveSSHCommand(ctx))
	if err != nil {
		return Identity{}, false
	}
	globalPath := userGlobalGitConfigPath()
	if globalPath == "" {
		globalPath = "/dev/null"
	}
	cacheKey := identityCacheKey(bin, prefix, globalPath, repoDir)
	if cacheKey != "" && ctx.Err() == nil {
		if cached, hit := identityCache.Load(cacheKey); hit {
			res := cached.(cachedIdentity)
			return res.id, res.ok
		}
	}

	queryCtx, cancel := context.WithTimeout(ctx, identityQueryTimeout)
	defer cancel()

	// Retain identity precedence while applying behavior hardening.
	out, code, err := exec.Run(queryCtx, bin, identityQueryArgv(prefix), exec.ExecOpts{
		Launch:         exec.HostLaunch("git_identity_query"),
		Dir:            repoDir,
		Timeout:        identityQueryTimeout,
		MaxOutputBytes: 64 << 10,
		AppendEnv: append([]string{
			"GIT_CONFIG_GLOBAL=" + globalPath,
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_PAGER=cat",
			"GIT_EXEC_PATH=" + gitExecPath(bin),
		}, bundledRuntimeEnv(bin)...),
	})
	if err != nil {
		return Identity{}, false
	}
	if code != 0 {
		if code == 1 && cacheKey != "" {
			identityCache.Store(cacheKey, cachedIdentity{})
		}
		return Identity{}, false
	}
	id := parseIdentity(out)
	if id.Name == "" || id.Email == "" {
		if cacheKey != "" {
			identityCache.Store(cacheKey, cachedIdentity{})
		}
		return Identity{}, false
	}
	if cacheKey != "" {
		identityCache.Store(cacheKey, cachedIdentity{id: id, ok: true})
	}
	return id, true
}

func identityCacheKey(bin string, prefix []string, globalPath, repoDir string) string {
	repo, inRepo := gitrepo.Discover(repoDir)
	if !inRepo {
		return ""
	}
	files := repo.LocalConfigFiles()
	if len(files) == 0 {
		return ""
	}
	sum := sha256.New()
	sum.Write([]byte(bin))
	sum.Write([]byte{0})
	sum.Write([]byte(strings.Join(prefix, "\x00")))
	sum.Write([]byte{0})
	sum.Write([]byte(globalPath))
	sum.Write([]byte{0})
	if !hashIdentityConfigFile(sum, globalPath) {
		return ""
	}
	for _, path := range files {
		sum.Write([]byte{0})
		sum.Write([]byte(path))
		sum.Write([]byte{0})
		if !hashIdentityConfigFile(sum, path) {
			return ""
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func hashIdentityConfigFile(sum hash.Hash, path string) bool {
	if path == "" || path == "/dev/null" {
		sum.Write([]byte("absent"))
		return true
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if strings.Contains(strings.ToLower(string(raw)), "include") {
			return false
		}
		sum.Write([]byte(strconv.Itoa(len(raw))))
		sum.Write([]byte{0})
		sum.Write(raw)
		return true
	case errors.Is(err, fs.ErrNotExist):
		sum.Write([]byte("absent"))
		return true
	default:
		return false
	}
}

// identityQueryArgv reads both identity keys in one invocation.
func identityQueryArgv(prefix []string) []string {
	out := append([]string{}, prefix...)
	return append(out, "config", "--get-regexp", "^user\\.(name|email)$")
}

func parseIdentity(out []byte) Identity {
	var id Identity
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "user.name":
			id.Name = value
		case "user.email":
			id.Email = value
		}
	}
	return id
}
