package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestCorpusExportsCallableVocabularyWithoutChangingRelease(t *testing.T) {
	before, err := turnload.LoadCatalog()
	testutil.FailErr(t, "load released decision catalog", err)
	out, err := buildCorpus()
	testutil.FailErr(t, "build training corpus", err)
	if out.Schema != "pw-decide-corpus/2" || len(out.Tools) == 0 || len(out.Surfaces) == 0 || len(out.Units) == 0 {
		t.Fatalf("incomplete versioned corpus: %+v", out)
	}
	tools := make(map[string]bool, len(out.Tools))
	for _, tool := range out.Tools {
		if toolcontract.IsCommandOperation(tool.Name) {
			t.Errorf("command operation %s exported as callable", tool.Name)
		}
		tools[tool.Name] = true
		if out.Questions.Tools.Options[tool.Name] != tool.Option || tool.Card == "" {
			t.Errorf("tool %s has inconsistent training text", tool.Name)
		}
	}
	if !tools["command"] || !tools["complete_leg"] {
		t.Fatalf("coordinator and worker vocabulary missing: %v", tools)
	}
	if len(tools) != len(out.Questions.Tools.Options) {
		t.Fatal("preload options differ from callable tools")
	}
	for _, surface := range out.Surfaces {
		for _, name := range append(surface.Floor, surface.Loadable...) {
			if !tools[name] {
				t.Errorf("surface %s references absent tool %s", surface.ID, name)
			}
		}
	}
	after, err := turnload.LoadCatalog()
	testutil.FailErr(t, "reload released decision catalog", err)
	if !reflect.DeepEqual(before.Turn.Tools.Options, after.Turn.Tools.Options) {
		t.Fatal("training export changed the released preload vocabulary")
	}
	encoded, err := json.Marshal(out)
	testutil.FailErr(t, "encode corpus", err)
	var decoded corpus
	testutil.FailErr(t, "decode corpus", json.Unmarshal(encoded, &decoded))
	if !reflect.DeepEqual(out, decoded) {
		t.Fatal("corpus contract changed during serialization")
	}
}
