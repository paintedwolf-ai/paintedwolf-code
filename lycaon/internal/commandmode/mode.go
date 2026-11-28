// Package commandmode resolves bound native command arguments to their declared catalog effect modes.
package commandmode

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// BoundInvocation names the catalog command and effect mode a command invocation selects.
type BoundInvocation struct {
	Command string
	Mode    string
}

// SelectMultiMode returns the effect mode a bound multi-mode command selects, or false when unknown.
func SelectMultiMode(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) (string, bool) {
	if len(spec.Effects) < 2 {
		return "", false
	}
	hasOpt := func(key string) bool {
		_, ok := bound.Last(key)
		return ok
	}
	switch spec.Name {
	case "tar":
		if hasOpt("-t") {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "zip", "unzip":
		if hasOpt("-l") || (spec.Name == "unzip" && hasOpt("-p")) {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "gzip":
		if hasOpt("-t") || hasOpt("-c") {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "gunzip":
		if hasOpt("-c") || len(bound.Operands()) == 0 {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "code-rewrite", "patch":
		if hasOpt("--dry-run") {
			return "dry-run", true
		}
		return "mutation", true
	case "yq":
		if hasOpt("-i") {
			return "-i", true
		}
		return "query", true
	case "find":
		return FindMode(bound)
	case "sort", "iconv":
		if hasOpt("-o") {
			return "-o", true
		}
		return "always", true
	case "uniq":
		if len(bound.Operands()) > 1 {
			return "OUTPUT-supplied", true
		}
		return "stdin-input-only", true
	case "env":
		for _, op := range bound.Operands() {
			if strings.IndexByte(op.Value, '=') <= 0 {
				return "dispatch", true
			}
		}
		return "inspect", true
	case "git rm", "git mv":
		if hasOpt("-n") {
			return "dry-run", true
		}
		return "mutation", true
	case "git apply":
		if (hasOpt("--check") || hasOpt("--stat") || hasOpt("--numstat") || hasOpt("--summary")) &&
			!hasOpt("--index") && !hasOpt("--cached") && !hasOpt("-3") {
			return "read-only", true
		}
		return "mutation", true
	case "curl":
		return curlModeFromBound(bound), true
	default:
		return "", false
	}
}

// FindMode returns the effect mode a bound find invocation selects.
func FindMode(bound commandprogram.BoundArgv) (string, bool) {
	if bound.Command != "find" {
		return "", false
	}
	if _, ok := bound.Last("-delete"); ok {
		return "-delete", true
	}
	if _, ok := bound.Last("-exec"); ok {
		return "-exec", true
	}
	return "default-predicates-print", true
}

// BindSearchArgv binds find and search invocations with negative -depth counts preserved.
func BindSearchArgv(spec toolcontract.CommandSpecification, argv commandprogram.Fields) (commandprogram.BoundArgv, error) {
	if spec.Name != "find" {
		return commandprogram.CatalogBinder{}.Bind(spec, argv)
	}
	var rewritten commandprogram.Fields
	var restore map[int]string
	for i := 1; i+1 < len(argv); i++ {
		if argv[i].Value == "--" {
			break
		}
		next := argv[i+1].Value
		if argv[i].Value == "-depth" && len(next) > 1 && next[0] == '-' && isASCIIDigits(next[1:]) {
			if rewritten == nil {
				rewritten = append(commandprogram.Fields(nil), argv...)
				restore = map[int]string{}
			}
			restore[i+1] = next
			rewritten[i+1].Value = "+" + next[1:]
		}
	}
	if rewritten == nil {
		return commandprogram.CatalogBinder{}.Bind(spec, argv)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, rewritten)
	for i := range bound.Items {
		if op := bound.Items[i].Operand; op != nil {
			for _, orig := range restore {
				if op.Value == "+"+orig[1:] {
					op.Value = orig
					break
				}
			}
		}
	}
	return bound, err
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// CurlTransferMode classifies a curl invocation from its jar and file-output options.
func CurlTransferMode(managedCookies, hasCaptures bool, destinations ...string) string {
	updatesJar := managedCookies || hasCaptures
	for _, dest := range destinations {
		if dest != "" && dest != "-" && dest != "/dev/null" {
			if updatesJar {
				return "managed-jar-update-to-file"
			}
			return "-o--D--c-to-file"
		}
	}
	if updatesJar {
		return "managed-jar-update"
	}
	return "request"
}

func curlModeFromBound(bound commandprogram.BoundArgv) string {
	managedCookies := false
	if opt, ok := bound.Last("--managed-cookie-jar"); ok && len(opt.Values) > 0 && opt.Values[0].Value != "" {
		managedCookies = true
	}
	hasCaptures := len(bound.Options("--capture-token")) > 0
	var dests [3]string
	for i, key := range []string{"-o", "-D", "-c"} {
		if opt, ok := bound.Last(key); ok && len(opt.Values) > 0 {
			dests[i] = opt.Values[0].Value
		}
	}
	return CurlTransferMode(managedCookies, hasCaptures, dests[:]...)
}
