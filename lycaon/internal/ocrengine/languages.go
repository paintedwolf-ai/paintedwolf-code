package ocrengine

import (
	"fmt"
	"regexp"
)

var languageCode = regexp.MustCompile(`^[a-z]{3}(?:_[a-z]{3,4})?$`)

// The profile is a build input, never a request to load system or downloaded data.
func (p *Pin) validateLanguages() error {
	if len(p.RecognitionLanguages) == 0 || len(p.RecognitionLanguages) > 8 {
		return fmt.Errorf("ocrengine: recognition_languages must contain one to eight languages")
	}
	seen := map[string]bool{}
	for _, language := range p.RecognitionLanguages {
		if !languageCode.MatchString(language) || language == "osd" || seen[language] {
			return fmt.Errorf("ocrengine: invalid or repeated recognition language")
		}
		seen[language] = true
	}
	if !seen["eng"] || len(p.TrainedData) != len(seen)+1 {
		return fmt.Errorf("ocrengine: trained_data must contain English, orientation and exactly the selected languages")
	}
	seen["osd"] = true
	supplemental := false
	for language := range seen {
		data, ok := p.TrainedData[language+"_traineddata"]
		if !ok || !validDigest(data.SHA256) || data.Bytes <= 0 {
			return fmt.Errorf("ocrengine: recognition profile needs pinned trained data for %s", language)
		}
		if (language == "eng" || language == "osd") && data.Source != "" {
			return fmt.Errorf("ocrengine: base models must come from the producer archive")
		}
		switch data.Source {
		case "":
		case "tessdata-fast":
			supplemental = true
		default:
			return fmt.Errorf("ocrengine: unknown trained-data source for %s", language)
		}
	}
	if supplemental {
		if p.TrainedDataSource.Repository != "tesseract-ocr/tessdata_fast" || !validCommit(p.TrainedDataSource.Commit) {
			return fmt.Errorf("ocrengine: supplemental models need the immutable tessdata_fast source")
		}
	} else if p.TrainedDataSource.Repository != "" || p.TrainedDataSource.Commit != "" {
		return fmt.Errorf("ocrengine: trained-data source is unused")
	}
	return nil
}
