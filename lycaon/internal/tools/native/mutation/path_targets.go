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
		option("-T", false)
		option("-X", false)
		if flag("-c") {
			// Members are read under -C, the way tar changes directory first.
			dir := ""
			if value, ok := argv.Last("-C"); ok && len(value.Values) > 0 {
				dir = value.Values[0].Value
			}
			for _, path := range operands {
				add(inDir(dir, path), false)
			}
		}
	case "patch":
		// Sections name their own files inside the patch; those bind when it
		// is read. These are the names argv itself gives.
		dir := ""
		if value, ok := argv.Last("-d"); ok && len(value.Values) > 0 {
			dir = value.Values[0].Value
		}
		for _, item := range argv.Items {
			if item.Option != nil && item.Option.Key == "-i" {
				for _, value := range item.Option.Values {
					add(inDir(dir, value.Value), false)
				}
			}
		}
		if len(operands) > 0 {
			add(inDir(dir, operands[0]), !flag("--dry-run"))
		}
		if len(operands) > 1 {
			add(inDir(dir, operands[1]), false)
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
