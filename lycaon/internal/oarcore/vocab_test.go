package oarcore

// TestVocabularyMatchesPublished fails when vocab.go and the vendored
// schemas/oar/vocabulary.yaml (refreshed by ./task oar:vendor) differ in any
// member or type, in either direction.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// vocabularyDoc mirrors the shape of the published vocabulary.yaml.
type vocabularyDoc struct {
	OAR  string `yaml:"oar"`
	Core struct {
		Facts []vocabularyFact `yaml:"facts"`
	} `yaml:"core"`
	Profiles []struct {
		Name  string           `yaml:"name"`
		Facts []vocabularyFact `yaml:"facts"`
	} `yaml:"profiles"`
	Anchors []struct {
		Name string `yaml:"name"`
	} `yaml:"anchors"`
	Detectors []struct {
		Ref string `yaml:"ref"`
	} `yaml:"detectors"`
	OnFire []struct {
		Action string  `yaml:"action"`
		Writes *string `yaml:"writes"`
	} `yaml:"on_fire"`
}

type vocabularyFact struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
}

func loadPublishedVocabulary(t *testing.T) vocabularyDoc {
	t.Helper()
	path := filepath.Join("..", "..", "..", "schemas", "oar", "vocabulary.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vendored vocabulary: %v", err)
	}
	var doc vocabularyDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

// splitFactsAndFunctions partitions a published member list on the spelling of
// its type: a plain fact type is a fact, a "(arg) -> ret" signature is an
// observation function. The signature grammar is the one the capability loader
// applies (fnSigRE), so both readers agree about what a function is.
func splitFactsAndFunctions(t *testing.T, members []vocabularyFact) (map[string]FactType, map[string]FunctionSig) {
	t.Helper()
	facts := map[string]FactType{}
	functions := map[string]FunctionSig{}
	for _, m := range members {
		if sig := fnSigRE.FindStringSubmatch(m.Type); sig != nil {
			functions[m.Name] = FunctionSig{Arg: FactType(sig[1]), Ret: FactType(sig[2])}
			continue
		}
		if !isFactType(m.Type) {
			t.Fatalf("published member %s declares type %q, which is neither a fact type nor a function signature", m.Name, m.Type)
		}
		facts[m.Name] = FactType(m.Type)
	}
	return facts, functions
}

func TestVocabularyMatchesPublished(t *testing.T) {
	doc := loadPublishedVocabulary(t)

	if want := fmt.Sprintf("%d.%d", oarMajor, oarMinor); doc.OAR != want {
		t.Errorf("vocabulary oar = %q, this engine implements %q", doc.OAR, want)
	}

	// Core tier ([OAR-FACT-15], [OAR-FIRE-11]).
	publishedCoreFacts, publishedCoreFns := splitFactsAndFunctions(t, doc.Core.Facts)
	compareFactMaps(t, "core", publishedCoreFacts, coreFacts)
	compareFunctionMaps(t, "core", publishedCoreFns, coreFunctions)

	// Profiles ([OAR-FACT-16]): same set of profiles, and each provided whole.
	publishedProfiles := map[string]bool{}
	for _, p := range doc.Profiles {
		publishedProfiles[p.Name] = true
		def, transcribed := profiles[p.Name]
		if !transcribed {
			t.Errorf("published profile %q is not transcribed in vocab.go", p.Name)
			continue
		}
		facts, functions := splitFactsAndFunctions(t, p.Facts)
		compareFactMaps(t, "profile "+p.Name, facts, def.Facts)
		compareFunctionMaps(t, "profile "+p.Name, functions, def.Functions)
	}
	for name := range profiles {
		if !publishedProfiles[name] {
			t.Errorf("vocab.go transcribes profile %q, which the published vocabulary does not define", name)
		}
	}

	// Core anchors ([OAR-PROF-1]) — same names, same specification order.
	if len(doc.Anchors) != len(coreAnchors) {
		t.Errorf("published vocabulary defines %d anchors, vocab.go transcribes %d", len(doc.Anchors), len(coreAnchors))
	} else {
		for i, a := range doc.Anchors {
			if a.Name != coreAnchors[i] {
				t.Errorf("anchor %d: published %q, transcribed %q", i, a.Name, coreAnchors[i])
			}
		}
	}

	// Reserved detectors ([OAR-CONF-25]).
	published := map[string]bool{}
	for _, d := range doc.Detectors {
		published[d.Ref] = true
		if !contains(reservedDetectors, d.Ref) {
			t.Errorf("published reserved detector %q is not transcribed", d.Ref)
		}
	}
	for _, ref := range reservedDetectors {
		if !published[ref] {
			t.Errorf("vocab.go transcribes reserved detector %q, which the published vocabulary does not define", ref)
		}
	}

	// on_fire vocabulary and the fact each action writes ([OAR-FIRE-1],
	// [OAR-FIRE-3]).
	publishedActions := map[string]bool{}
	for _, a := range doc.OnFire {
		publishedActions[a.Action] = true
		if !contains(onFireActions, a.Action) {
			t.Errorf("published on_fire action %q is not transcribed", a.Action)
			continue
		}
		wantWrites := ""
		if a.Writes != nil {
			wantWrites = *a.Writes
		}
		if got, ok := onFireWrites[a.Action]; !ok || got != wantWrites {
			t.Errorf("on_fire %s writes %q in vocab.go, published vocabulary says %q", a.Action, got, wantWrites)
		}
	}
	for _, action := range onFireActions {
		if !publishedActions[action] {
			t.Errorf("vocab.go transcribes on_fire action %q, which the published vocabulary does not define", action)
		}
	}
}

func compareFactMaps(t *testing.T, where string, published, transcribed map[string]FactType) {
	t.Helper()
	for name, typ := range published {
		got, ok := transcribed[name]
		if !ok {
			t.Errorf("%s: published fact %s is not transcribed in vocab.go", where, name)
			continue
		}
		if got != typ {
			t.Errorf("%s: fact %s is transcribed as %s, published vocabulary says %s", where, name, got, typ)
		}
	}
	for name := range transcribed {
		if _, ok := published[name]; !ok {
			t.Errorf("%s: vocab.go transcribes fact %s, which the published vocabulary does not define", where, name)
		}
	}
}

func compareFunctionMaps(t *testing.T, where string, published, transcribed map[string]FunctionSig) {
	t.Helper()
	for name, sig := range published {
		got, ok := transcribed[name]
		if !ok {
			t.Errorf("%s: published function %s is not transcribed in vocab.go", where, name)
			continue
		}
		if got != sig {
			t.Errorf("%s: function %s is transcribed as (%s) -> %s, published vocabulary says (%s) -> %s",
				where, name, got.Arg, got.Ret, sig.Arg, sig.Ret)
		}
	}
	for name := range transcribed {
		if _, ok := published[name]; !ok {
			t.Errorf("%s: vocab.go transcribes function %s, which the published vocabulary does not define", where, name)
		}
	}
}
