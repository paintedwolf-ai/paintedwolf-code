package mutation

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// mutationPathTargets records the exact operands the mutation owners bind as
// files. Member names, modes, templates and replacement text are not paths.
func mutationPathTargets(argv commandprogram.BoundArgv) []commandprogram.PathTarget {
	targets := []commandprogram.PathTarget{}
	add := func(path string, write bool) {
		if path != "" && path != "-" {
			targets = append(targets, commandprogram.PathTarget{Path: path, Write: write})
		}
	}
	option := func(key string, write bool) {
		for _, item := range argv.Items {
			if item.Option != nil && item.Option.Key == key {
				for _, value := range item.Option.Values {
					add(value.Value, write)
				}
			}
		}
	}
	flag := func(key string) bool { _, ok := argv.Last(key); return ok }
	operands := argv.Operands().Values()
	switch argv.Command {
	case "rm", "rmdir", "mkdir", "tee":
		for _, path := range operands {
			add(path, true)
		}
	case "mv":
		for _, path := range operands {
			add(path, true)
		}
	case "cp":
		for i, path := range operands {
			add(path, i == len(operands)-1)
		}
	case "chmod", "chown":
		for _, path := range operands[min(1, len(operands)):] {
			add(path, true)
		}
	case "touch":
		option("-r", false)
		for _, path := range operands {
			add(path, true)
		}
	case "mktemp":
		// The template is a name beneath the session scratch directory.
	case "tar":
		option("-C", flag("-x"))
		option("-f", flag("-c"))
		if flag("-c") {
			for _, path := range operands {
				add(path, false)
			}
		}
	case "zip":
		if len(operands) > 0 {
			add(operands[0], true)
		}
		for _, path := range operands[min(1, len(operands)):] {
			add(path, false)
		}
	case "unzip":
		if len(operands) > 0 {
			add(operands[0], false)
		}
		option("-d", true)
	case "gzip", "gunzip", "zcat":
		stream := argv.Command == "zcat" || flag("-c") || flag("-t") || flag("-l")
		for _, path := range operands {
			decompress := argv.Command == "gunzip" || flag("-d") || flag("-t")
			if !stream && !decompress && strings.HasSuffix(path, ".gz") {
				continue
			}
			if !stream && decompress {
				if _, ok := gunzipOutput(path); !ok {
					continue
				}
			}
			add(path, !stream && !flag("-k"))
			if stream {
				continue
			}
			output := path + ".gz"
			if decompress {
				var ok bool
				output, ok = gunzipOutput(path)
				if !ok {
					continue
				}
			}
			add(output, true)
		}
	case "edit", "replace-lines":
		option("--path", true)
	case "code-rewrite":
		option("--path", !flag("--dry-run"))
	}
	return targets
}
