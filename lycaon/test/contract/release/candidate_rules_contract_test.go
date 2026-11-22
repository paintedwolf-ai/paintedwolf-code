package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
	type ruleBlob struct {
		tag  string
		file string
		sha  string
	}
	var rules []ruleBlob
	var shas []string
	for _, tag := range postV1Tags(t, root) {
		listing := gitOutput(t, root, "ls-tree", "-r", tag, "--", "lycaon/config/packs")
		for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
			meta, file, ok := strings.Cut(line, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 || path.Base(path.Dir(file)) != "policy" || path.Ext(file) != ".yaml" || seen[fields[2]] {
				continue
			}
			seen[fields[2]] = true
			rules = append(rules, ruleBlob{tag: tag, file: file, sha: fields[2]})
			shas = append(shas, fields[2])
		}
	}
	blobs := gitCatFileBatch(t, root, shas)
	read := 0
	for _, rule := range rules {
		data := blobs[rule.sha]
		upgraded, changed, err := oardraft.Upgrade(data)
		if err != nil {
			t.Errorf("%s %s: %v", rule.tag, rule.file, err)
			continue
		}
		if !changed {
			continue
		}
		read++
		var doc any
		contractcheck.FailErr(t, "decode "+rule.file, yaml.Unmarshal(upgraded, &doc))
		instance, err := jsonInstance(doc)
		contractcheck.FailErr(t, "convert "+rule.file, err)
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s %s: upgraded rule does not satisfy OAR 1.0: %v", rule.tag, rule.file, err)
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

func gitCatFileBatch(t *testing.T, root string, shas []string) map[string][]byte {
	t.Helper()
	if len(shas) == 0 {
		return map[string][]byte{}
	}
	var input bytes.Buffer
	for _, sha := range shas {
		input.WriteString(sha)
		input.WriteByte('\n')
	}
	out, code, err := gitexec.Run(t.Context(), root, []string{"cat-file", "--batch"}, gitexec.Opts{
		Input:     input.Bytes(),
		MaxOutput: 64 << 20,
	})
	if err == nil && code != 0 {
		err = fmt.Errorf("%s", bytes.TrimSpace(out))
	}
	contractcheck.FailErr(t, "git cat-file --batch", err)
	blobs := make(map[string][]byte, len(shas))
	raw := out
	for len(raw) > 0 {
		headerEnd := bytes.IndexByte(raw, '\n')
		if headerEnd < 0 {
			t.Fatal("git cat-file --batch: truncated header")
		}
		fields := strings.Fields(string(raw[:headerEnd]))
		raw = raw[headerEnd+1:]
		if len(fields) != 3 {
			t.Fatalf("git cat-file --batch: unexpected header %q", strings.Join(fields, " "))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || len(raw) < size+1 || raw[size] != '\n' {
			t.Fatalf("git cat-file --batch: invalid payload for %s", fields[0])
		}
		blobs[fields[0]] = raw[:size:size]
		raw = raw[size+1:]
	}
	return blobs
}
