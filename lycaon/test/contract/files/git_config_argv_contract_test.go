package contract

import (
	"go/parser"
	"go/token"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGitConfigScannerRequiresCompleteReadArgv(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		refuse bool
	}{
		{"audit", `return []string{"config", "--local", "--list", "--name-only", "--null"}, nil`, false},
		{"audit-name write", `return []string{"config", "--local", "user.name", "Changed"}, nil`, true},
		{"list", `return []string{"config", "--local", "--list"}, nil`, false},
		{"get", `return []string{"config", "--local", "--get", "--", key}, nil`, false},
		{"regexp", `return []string{"config", "--local", "--get-regexp", "--", key}, nil`, false},
		{"write", `return []string{"config", "--local", "user.name", "Changed"}, nil`, true},
		{"unset", `return []string{"config", "--local", "--unset", key}, nil`, true},
		{"mixed", `return []string{"config", "--local", "--list", "--unset", key}, nil`, true},
		{"global", `return []string{"config", "--global", "--get", "--", key}, nil`, true},
		{"missing separator", `return []string{"config", "--local", "--get", key}, nil`, true},
		{"dynamic action", `return []string{"config", "--local", key}, nil`, true},
		{"dynamic tail", `return append([]string{"config", "--local", "--list"}, tail...), nil`, true},
		{"prefix", `return append([]string{"config", "--local", "--get", "--"}, tail...), nil`, true},
		{"extra operand", `return []string{"config", "--local", "--get", "--", key, key}, nil`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			name := "query"
			if tc.name == "audit-name write" {
				name = "repoConfigAuditArgv"
			}
			file, err := parser.ParseFile(fset, "fixture.go", "package fixture; func "+name+"(key string, tail []string) ([]string, error) {"+tc.body+"}", 0)
			contractcheck.FailErr(t, "parse config argument fixture", err)
			findings := scanGitOneDoor(fset, file, "fixture.go", false)
			if (len(findings) != 0) != tc.refuse {
				t.Fatalf("scanner findings %v; refuse=%v", findings, tc.refuse)
			}
		})
	}
}
