package conformance_test

import (
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

const textChangePatch = "--- a/sub/a.txt\n+++ b/sub/a.txt\n@@ -1,2 +1,2 @@\n-first\n+changed\n second\n"

var archiveFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "mutation-patch-forms", Commands: []string{"patch"},
			Forms:   []string{"patch:-p", "patch:-i", "patch:-R", "patch:-N", "patch:--dry-run", "patch:-d", "patch:-F", "patch:-s", "patch:-t", "patch:-f", "patch:-E", "patch:--no-backup-if-mismatch", "patch:-u"},
			Program: `patch -p 1 -i change.patch --dry-run -d . -F 0 -s -t -f -N --no-backup-if-mismatch -u && patch -s -p 1 -i change.patch && test "$(head -n 1 sub/a.txt)" = changed && patch -s -R -p 1 -i change.patch && patch -s -E -p 1 -i empty.patch && test ! -e empty-target.txt && printf 'patch-ok\n'`,
			Files: map[string]referenceuserland.Entry{
				"change.patch":     {Data: []byte(textChangePatch)},
				"empty-target.txt": {Data: []byte("one\n")},
				"empty.patch":      {Data: []byte("--- a/empty-target.txt\n+++ b/empty-target.txt\n@@ -1 +0,0 @@\n-one\n")},
			},
			Tracked: []string{"sub/a.txt"},
		},
		Stdout: "patch-ok\n", Files: map[string]string{"sub/a.txt": "first\nsecond\n"},
	},
	{
		Case: commandconformance.Case{
			ID: "archive-tar-selection-compression", Commands: []string{"tar"},
			Forms:   []string{"tar:-J", "tar:--zstd", "tar:-a", "tar:--exclude", "tar:-X", "tar:--exclude-vcs", "tar:-T"},
			Program: `tar -c -J -f compressed.tar.xz sub/a.txt && tar -t -J -f compressed.tar.xz && tar -c --zstd -f compressed.tar.zst sub/a.txt && tar -t --zstd -f compressed.tar.zst && tar -c -a -f selected.tar.gz --exclude sub/b.txt -X excludes.txt --exclude-vcs -T members.txt && tar -t -f selected.tar.gz`,
			Files: map[string]referenceuserland.Entry{
				"members.txt":  {Data: []byte("sub/a.txt\nsub/b.txt\n")},
				"excludes.txt": {Data: []byte("never.txt\n")},
			},
		},
		Stdout: "sub/a.txt\nsub/a.txt\nsub/a.txt\n",
	},
	{
		Case: commandconformance.Case{
			ID: "archive-zip-selection-paths", Commands: []string{"zip", "unzip"},
			Forms:   []string{"zip:-x", "zip:-j", "unzip:-x", "unzip:-j", "unzip:-n"},
			Program: `zip -q -j flat.zip sub/a.txt sub/b.txt -x sub/b.txt && unzip -q -j flat.zip -d unpacked -x b.txt && cat unpacked/a.txt && printf 'retained\n' > unpacked/a.txt && unzip -q -n -j flat.zip -d unpacked -x b.txt && cat unpacked/a.txt && test ! -e unpacked/b.txt`,
			Tracked: []string{"unpacked/a.txt"},
		},
		Stdout: "first\nsecond\nretained\n", Files: map[string]string{"unpacked/a.txt": "retained\n"},
	},
	{
		Case: commandconformance.Case{
			ID: "text-diff-selection-forms", Commands: []string{"diff"},
			Forms:   []string{"diff:-N", "diff:-x", "diff:-X", "diff:--strip-trailing-cr"},
			Program: `diff -N -x skip -X diff-excludes.txt --strip-trailing-cr left right >/dev/null; printf 'different:%s\n' "$?"`,
			Files: map[string]referenceuserland.Entry{
				"left/equal.txt": {Data: []byte("equal\r\n")}, "right/equal.txt": {Data: []byte("equal\n")},
				"left/only.txt": {Data: []byte("only left\n")}, "left/skip": {Data: []byte("ignored\n")},
				"diff-excludes.txt": {Data: []byte("ignored\n")},
			},
		},
		Stdout: "different:1\n",
	},
}
