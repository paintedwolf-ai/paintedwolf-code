package exec

import (
	"errors"
	"testing"
)

func TestValidateInlineEnvRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "1BAD", "bad-key", "spaced key"} {
		err := ValidateInlineEnv(map[string]string{key: "1"})
		if !errors.Is(err, ErrInvalidEnvKey) {
			t.Fatalf("key %q: err = %v, want ErrInvalidEnvKey", key, err)
		}
	}
}

func TestValidateInlineEnvAcceptsLowercaseKeysAndBlocksThemCaseInsensitively(t *testing.T) {
	if err := ValidateInlineEnv(map[string]string{"no_proxy": "localhost"}); err != nil {
		t.Fatalf("lowercase key: %v", err)
	}
	for _, key := range []string{"git_dir", "ld_preload", "node_options"} {
		if err := ValidateInlineEnv(map[string]string{key: "x"}); !errors.Is(err, ErrBlockedEnvKey) {
			t.Fatalf("key %q: err = %v, want ErrBlockedEnvKey", key, err)
		}
	}
}

func TestValidateInlineEnvRejectsBlockedKeys(t *testing.T) {
	for _, key := range []string{"GIT_DIR", "LD_PRELOAD", "NODE_OPTIONS", "BASH_ENV"} {
		err := ValidateInlineEnv(map[string]string{key: "x"})
		if !errors.Is(err, ErrBlockedEnvKey) {
			t.Fatalf("key %q: err = %v, want ErrBlockedEnvKey", key, err)
		}
	}
}

func TestValidateInlineEnvBlocksInlinePathOverride(t *testing.T) {
	for _, key := range []string{"PATH", "path", "Path"} {
		err := ValidateInlineEnv(map[string]string{key: "/tmp/hijack"})
		if !errors.Is(err, ErrBlockedEnvKey) {
			t.Fatalf("key %q: err = %v, want ErrBlockedEnvKey", key, err)
		}
	}
	// The trusted base environment supplies PATH.
	got := SanitizeEnviron([]string{"PATH=/usr/bin", "HOME=/home/user"})
	if envSliceToMap(got)["PATH"] != "/usr/bin" {
		t.Fatalf("SanitizeEnviron dropped PATH: %v", got)
	}
}

func TestValidateInlineEnvAcceptsValidKeys(t *testing.T) {
	if err := ValidateInlineEnv(map[string]string{"MY_FLAG": "1", "X": "y"}); err != nil {
		t.Fatalf("ValidateInlineEnv: %v", err)
	}
}

func TestBuildProcessEnvMergesInline(t *testing.T) {
	got, err := buildProcessEnv([]string{"PATH=/bin", "HOME=/tmp"}, map[string]string{"MY_FLAG": "yes"}, nil)
	if err != nil {
		t.Fatalf("buildProcessEnv: %v", err)
	}
	m := envSliceToMap(got)
	if m["MY_FLAG"] != "yes" {
		t.Fatalf("MY_FLAG = %q", m["MY_FLAG"])
	}
	if m["PATH"] != "/bin" {
		t.Fatalf("PATH = %q", m["PATH"])
	}
}

func TestBuildProcessEnvStripsBlockedInline(t *testing.T) {
	_, err := buildProcessEnv(nil, map[string]string{"GIT_EXEC_PATH": "/tmp"}, nil)
	if !errors.Is(err, ErrBlockedEnvKey) {
		t.Fatalf("err = %v, want ErrBlockedEnvKey", err)
	}
}
