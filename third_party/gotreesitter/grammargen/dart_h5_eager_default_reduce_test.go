package grammargen

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestDartH5EagerDefaultReduceParserProof(t *testing.T) {
	t.Setenv("GOT_EAGER_DEFAULT_REDUCE", "1")

	spec, ok := importParityGrammarByName("dart")
	if !ok {
		t.Fatal("missing dart import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Dart grammar source unavailable: %v", err)
	}
	genLang, err := generateWithTimeout(gram, 2*time.Minute)
	if err != nil {
		t.Fatalf("generate Dart: %v", err)
	}
	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, genLang)

	sample12 := dartH5ParsePair(t, "sample12", genLang, refLang, dartH5Sample12Source())
	sample21 := dartH5ParsePair(t, "sample21", genLang, refLang, dartH5Sample21Source())
	t.Logf("eager-default-reduce runtime=%d sample12=%s sample21=%s", genLang.StateCount, sample12.summary(), sample21.summary())
	if !sample12.ok() {
		t.Fatalf("sample12 mismatch with eager default reduce: %s", sample12.summary())
	}
	if !sample21.ok() {
		t.Fatalf("sample21 mismatch with eager default reduce: %s", sample21.summary())
	}
}

type dartH5PairResult struct {
	genType  string
	genStart uint32
	genEnd   uint32
	genErr   bool
	refType  string
	refStart uint32
	refEnd   uint32
	refErr   bool
	deep     int
}

func (r dartH5PairResult) ok() bool {
	return !r.genErr && !r.refErr && r.deep == 0 &&
		r.genType == r.refType &&
		r.genStart == r.refStart &&
		r.genEnd == r.refEnd
}

func (r dartH5PairResult) summary() string {
	return fmt.Sprintf("gen=%s[%d:%d]/err=%v ref=%s[%d:%d]/err=%v deep=%d",
		r.genType, r.genStart, r.genEnd, r.genErr, r.refType, r.refStart, r.refEnd, r.refErr, r.deep)
}

func dartH5ParsePair(t *testing.T, name string, genLang, refLang *gotreesitter.Language, src []byte) dartH5PairResult {
	t.Helper()
	genParser := gotreesitter.NewParser(genLang)
	if name == "sample12" && os.Getenv("DART_H5_TRACE") == "1" && os.Getenv("DART_H5_TRACE_VARIANT") == "eager-default-reduce" {
		genParser.SetGLRTrace(true)
	}
	refParser := gotreesitter.NewParser(refLang)
	genTree, err := genParser.Parse(src)
	if err != nil {
		t.Fatalf("%s generated parse: %v", name, err)
	}
	refTree, err := refParser.Parse(src)
	if err != nil {
		t.Fatalf("%s reference parse: %v", name, err)
	}
	genRoot, refRoot := genTree.RootNode(), refTree.RootNode()
	divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 20)
	for i, div := range divs {
		t.Logf("%s div[%d]=%s", name, i, div.String())
	}
	return dartH5PairResult{
		genType:  genRoot.Type(genLang),
		genStart: genRoot.StartByte(),
		genEnd:   genRoot.EndByte(),
		genErr:   genRoot.HasError(),
		refType:  refRoot.Type(refLang),
		refStart: refRoot.StartByte(),
		refEnd:   refRoot.EndByte(),
		refErr:   refRoot.HasError(),
		deep:     len(divs),
	}
}

func dartH5Sample12Source() []byte {
	return []byte(`bool? _boolAttribute(
    String resourceId,
    String name,
    Map<String, Object?> attributes,
    String attributeName,
    ) {
  final Object? value = attributes[attributeName];
  if (value == null) {
    return null;
  }
  if (value != 'true' && value != 'false') {
    throw L10nException(
      'The "$attributeName" value of the "$name" placeholder in message $resourceId '
          'must be a boolean value.',
    );
  }
  return value == 'true';
}
`)
}

func dartH5Sample21Source() []byte {
	return []byte(`bool f() {
  assert(textColor != null
      && style != null
      && margin != null
      && _position != null
      && _position.isFinite
      && _opacity != null
      && _opacity >= 0.0
      && _opacity <= 1.0);
  return true;
}
`)
}
