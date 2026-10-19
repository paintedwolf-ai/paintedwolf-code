package ocrengine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecognitionProfileRejectsUnpinnedUnsafeAndUnboundedLanguages(t *testing.T) {
	data := pinPayload{SHA256: strings.Repeat("a", 64), Bytes: 1}
	for _, languages := range [][]string{nil, {"eng", "eng"}, {"eng", "osd"}, {"eng", "../deu"}, {"eng", "deu"}, {"deu"}, {"eng", "eng+deu"}, {"eng", "deu", "fra", "spa", "ita", "por", "jpn", "kor", "rus"}} {
		pin := Pin{RecognitionLanguages: languages, TrainedData: map[string]pinPayload{"eng_traineddata": data, "osd_traineddata": data}}
		if err := pin.validateLanguages(); err == nil {
			t.Fatalf("invalid language profile accepted: %v", languages)
		}
	}
}

func TestEveryRecognitionLanguageIsVerifiedBeforeItsArgumentIsExposed(t *testing.T) {
	resetForTest()
	root, pin, plat := stagedPin(t)
	pin.RecognitionLanguages = []string{"eng", "deu", "chi_sim"}
	for _, language := range pin.RecognitionLanguages[1:] {
		data := writePayload(t, root, "tessdata/"+language+".traineddata", []byte(language), 0o644)
		pin.TrainedData[language+"_traineddata"] = pinPayload{SHA256: data.SHA256, Bytes: data.Bytes}
	}
	engine, err := verifyRoot(root, pin, plat)
	testutil.FailErr(t, "verify recognition profile", err)
	if engine.LanguageArgument != "eng+deu+chi_sim" {
		t.Fatalf("language argument = %q", engine.LanguageArgument)
	}
	for _, missing := range []bool{false, true} {
		resetForTest()
		path := filepath.Join(root, "tessdata/chi_sim.traineddata")
		if missing {
			testutil.FailErr(t, "remove language fixture", os.Remove(path))
		} else {
			testutil.FailErr(t, "corrupt language fixture", os.WriteFile(path, []byte("changed"), 0o644))
		}
		_, err := verifyRoot(root, pin, plat)
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("invalid language data accepted: %v", err)
		}
	}
}

func TestSupplementalModelsRequireImmutableSourceAndPreserveProducerBase(t *testing.T) {
	base := pinPayload{SHA256: strings.Repeat("a", 64), Bytes: 1}
	supplement := base
	supplement.Source = "tessdata-fast"
	p := Pin{RecognitionLanguages: []string{"eng", "deu"},
		TrainedDataSource: pinDataSource{Repository: "tesseract-ocr/tessdata_fast", Commit: strings.Repeat("a", 40)},
		TrainedData:       map[string]pinPayload{"eng_traineddata": base, "osd_traineddata": base, "deu_traineddata": supplement}}
	testutil.FailErr(t, "validate supplemental model pin", p.validateLanguages())
	for _, mutate := range []func(*Pin){
		func(p *Pin) { p.TrainedDataSource.Commit = "main" },
		func(p *Pin) { p.TrainedDataSource.Repository = "other/models" },
		func(p *Pin) { p.TrainedData["eng_traineddata"] = supplement },
		func(p *Pin) { p.TrainedData["osd_traineddata"] = supplement },
		func(p *Pin) { data := supplement; data.Source = "system"; p.TrainedData["deu_traineddata"] = data },
	} {
		candidate := p
		candidate.TrainedData = make(map[string]pinPayload)
		for key, value := range p.TrainedData {
			candidate.TrainedData[key] = value
		}
		mutate(&candidate)
		if err := candidate.validateLanguages(); err == nil {
			t.Fatal("invalid supplemental model source accepted")
		}
	}
}
