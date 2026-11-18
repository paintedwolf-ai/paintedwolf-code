package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oardraft"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every OAR 1.0 candidate rule a post-v1 release shipped is read in the OAR
// 1.0 shape: none is refused, and each upgraded document satisfies the
// vendored OAR 1.0 schema.
func TestReleasedCandidateRulesReadAsOAR10(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schema := compileVendoredOARSchema(t, filepath.Join(root, "schemas"))
	seen := map[string]bool{}
	read := 0
	for _, tag := range postV1Tags(t, root) {
		listing := gitOutput(t, root, "ls-tree", "-r", tag, "--", "lycaon/config/packs")
		for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
			meta, file, ok := strings.Cut(line, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 || path.Base(path.Dir(file)) != "policy" || path.Ext(file) != ".yaml" || seen[fields[2]] {
				continue
			}
			seen[fields[2]] = true
			data := gitOutput(t, root, "cat-file", "blob", fields[2])
			upgraded, changed, err := oardraft.Upgrade(data)
			if err != nil {
				t.Errorf("%s %s: %v", tag, file, err)
				continue
			}
			if !changed {
				continue
			}
			read++
			var doc any
			contractcheck.FailErr(t, "decode "+file, yaml.Unmarshal(upgraded, &doc))
			instance, err := jsonInstance(doc)
			contractcheck.FailErr(t, "convert "+file, err)
			if err := schema.Validate(instance); err != nil {
				t.Errorf("%s %s: upgraded rule does not satisfy OAR 1.0: %v", tag, file, err)
			}
		}
	}
	if read == 0 {
		t.Fatal("no released candidate rule was read; fetch tags")
	}
}

func compileVendoredOARSchema(t *testing.T, schemaDir string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"oar.schema.json", "selector.schema.json"} {
		raw, err := os.ReadFile(filepath.Join(schemaDir, "oar", name))
		contractcheck.FailErr(t, "read "+name, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		contractcheck.FailErr(t, "parse "+name, err)
		contractcheck.FailErr(t, "add "+name, compiler.AddResource(oar.SchemaBaseURL+name, doc))
		contractcheck.FailErr(t, "add file "+name, compiler.AddResource(name, doc))
	}
	schema, err := compiler.Compile(oar.SchemaBaseURL + "oar.schema.json")
	contractcheck.FailErr(t, "compile OAR schema", err)
	return schema
}

// jsonInstance gives the validator the JSON value model it expects.
func jsonInstance(doc any) (any, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

func postV1Tags(t *testing.T, root string) []string {
	t.Helper()
	first := semver.MustParse("1.0.0")
	var out []string
	for _, tag := range strings.Fields(string(gitOutput(t, root, "tag", "--merged", "HEAD", "--list", "v*"))) {
		if version, err := semver.StrictNewVersion(strings.TrimPrefix(tag, "v")); err == nil && !version.LessThan(first) {
			out = append(out, tag)
		}
	}
	if len(out) == 0 {
		t.Fatal("no post-v1 tag is merged into HEAD; fetch tags")
	}
	return out
}

func gitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	out, code, err := gitexec.Run(t.Context(), root, args, gitexec.Opts{})
	if err == nil && code != 0 {
		err = fmt.Errorf("%s", bytes.TrimSpace(out))
	}
	contractcheck.FailErr(t, "git "+strings.Join(args, " "), err)
	return out
}
