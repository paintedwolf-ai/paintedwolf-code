package commandconformance

import (
	"archive/tar"
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

// The data files live beside the corpus; capture rewrites them from the module root.
const (
	SnapshotsPath = "test/commandconformance/testdata/snapshots.json"
	GitSeedPath   = "test/commandconformance/testdata/git-seed.json"
)

//go:embed testdata/*.json
var testdata embed.FS

// embedded is the name of a data file inside the package's embedded files.
func embedded(modulePath string) string {
	return "testdata/" + path.Base(modulePath)
}

var (
	pngSample  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03}
	binaryTODO = []byte("TODO\x00binary\n")
)

// seedWorkspace is the workspace every case starts from.
var seedWorkspace = map[string]referenceuserland.Entry{
	"notes.txt":          text("alpha line 1\nbeta line 2\n\nalpha line 4\ngamma\twith\ttabs\n"),
	"numbers.txt":        text("10\n2\n30\n2\n"),
	"table.tsv":          text("id\tname\tcount\n1\talpha\t10\n2\tbeta\t20\n3\tgamma\t30\n"),
	"config.json":        text(`{"enabled":false,"version":1,"items":["a","b"]}` + "\n"),
	"data.yaml":          text("enabled: false\nversion: 1\n"),
	"code.go":            text("package sample\n\n// Alpha returns 1.\nfunc Alpha() int {\n\treturn 1\n}\n"),
	"patterns.txt":       text("alpha\ngamma\n"),
	"sub/a.txt":          text("first\nsecond\n"),
	"sub/b.txt":          text("first\nthird\n"),
	"sub/nested/deep.go": text("package deep\n\n// TODO: fix OldName\nfunc OldName() {}\n"),
	"sub/script.sh":      text("EXPORTED_FROM_SOURCE=sourced_ok\nprintf 'from-source:%s\\n' \"$EXPORTED_FROM_SOURCE\"\n"),
	"sub/bad.sh":         text("if true; then\n"),
	".hidden.txt":        text("secret-dotfile\n"),
	"empty.txt":          text(""),
	"binary.bin":         {Data: pngSample},
}

func text(s string) referenceuserland.Entry {
	return referenceuserland.Entry{Data: []byte(s)}
}

// GitSeed is the recorded repository a Git case starts from: the seed workspace's
// notes.txt, sub/a.txt and sub/b.txt committed once on main.
type GitSeed map[string]referenceuserland.Entry

// gitSeedProgram makes the repository in the GNU userland and writes its .git as a tar stream.
const gitSeedProgram = `export GIT_AUTHOR_DATE=2025-01-01T00:00:00Z GIT_COMMITTER_DATE=2025-01-01T00:00:00Z
git init -q -b main
git config user.name 'Painted Wolf test'
git config user.email test@paintedwolf.dev
git add sub/a.txt sub/b.txt notes.txt
git commit -q -m 'initial fixture commit'
rm -rf .git/hooks
tar --sort=name -cf - .git | base64 -w 0`

// LoadGitSeed reads the recorded repository.
func LoadGitSeed() (GitSeed, error) {
	data, err := testdata.ReadFile(embedded(GitSeedPath))
	if err != nil {
		return nil, err
	}
	return decodeGitSeed(data)
}

func decodeGitSeed(data []byte) (GitSeed, error) {
	var files map[string]*File
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("decode the Git seed: %w", err)
	}
	seed := GitSeed{}
	for name, f := range files {
		if f == nil {
			return nil, fmt.Errorf("git seed entry %s is empty", name)
		}
		seed[name] = f.Entry()
	}
	return seed, nil
}

// CaptureGitSeed makes the repository in the GNU userland.
func CaptureGitSeed(ctx context.Context, r References) (map[string]*File, error) {
	results, err := r.Run(ctx, referenceuserland.GNU, []referenceuserland.Case{{Program: gitSeedProgram, Workspace: seedWorkspace}})
	if err != nil {
		return nil, err
	}
	if results[0].Status != 0 {
		return nil, fmt.Errorf("git seed exited %d: %s", results[0].Status, results[0].Stderr)
	}
	archive, err := base64.StdEncoding.DecodeString(string(results[0].Stdout))
	if err != nil {
		return nil, err
	}
	files := map[string]*File{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean(header.Name)
		mode := fs.FileMode(header.Mode).Perm()
		switch header.Typeflag {
		case tar.TypeDir:
			files[name] = &File{Mode: Mode(fs.ModeDir | mode)}
		case tar.TypeReg:
			data, err := io.ReadAll(reader)
			if err != nil {
				return nil, err
			}
			files[name] = &File{Mode: Mode(mode), Data: data}
		default:
			return nil, fmt.Errorf("git seed entry %s has unsupported type %q", name, header.Typeflag)
		}
	}
}
