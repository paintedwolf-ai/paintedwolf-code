package oar

import (
	"bytes"
	"errors"
	"github.com/lycaon/lycaon/internal/configlayout"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestPortablePackIsPortable(t *testing.T) {
	ensureCatalog(t)
	dir := filepath.Join(configlayout.FindModuleRoot(), "config", "oar-portable-pack")
	profileRaw, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	testutil.FailErr(t, "read profile map", err)
	var profileMap AnchorProfileMap
	testutil.FailErr(t, "decode profile map", yaml.Unmarshal(profileRaw, &profileMap))
	decl := &EngineDeclaration{
		Host: "example", ProfileMap: profileMap, Profiles: []string{ProfileContentSafety, ProfileMCP},
		Window: 16, ExpressionNodesMax: 256,
		Detectors: []string{"detector://noop", "detector://error", "detector://fixture"},
	}
	_, err = decl.Environment()
	testutil.FailErr(t, "portable engine declaration", err)
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	l.SetDetectors(corpusReservedDetectors())
	l.SetDeclaration(decl)
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "readdir", err)
	var rules []*Rule
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".yaml") || name == "profile.yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		testutil.FailErr(t, "read "+name, err)
		loaded, err := l.loadPortableYAML(raw)
		testutil.FailErr(t, "load "+name, err)
		rules = append(rules, loaded...)
	}
	if len(rules) == 0 {
		t.Fatal("portable pack loaded no rules")
	}
	for _, r := range rules {
		rep := LintPortability(r)
		if !rep.Portable {
			t.Errorf("%s is not portable: host_anchor=%q host_facts=%v", r.Qualified(), rep.HostAnchor, rep.HostFacts)
		}
	}
	outcomes, err := RunCorpusDir(filepath.Join(dir, "fixtures"))
	testutil.FailErr(t, "run pack fixtures", err)
	if len(outcomes) == 0 {
		t.Fatal("portable pack carries no fixtures")
	}
	for _, o := range outcomes {
		if o.Status != "pass" {
			t.Errorf("fixture %s: %s", o.ID, o.Reason)
		}
	}
}

func (l *Loader) loadPortableYAML(raw []byte) ([]*Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []*Rule
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(doc) == 0 {
			continue
		}
		id, _ := doc["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		rule, skip, err := l.parseRule(id, doc)
		if err != nil {
			return nil, err
		}
		if skip || rule == nil {
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}
