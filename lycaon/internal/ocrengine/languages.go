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
	for language := range seen {
		data, ok := p.TrainedData[language+"_traineddata"]
		if !ok || !validDigest(data.SHA256) || data.Bytes <= 0 {
			return fmt.Errorf("ocrengine: recognition profile needs pinned trained data for %s", language)
		}
	}
	return nil
}
