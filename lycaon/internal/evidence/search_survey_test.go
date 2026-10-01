package evidence

import "testing"

func TestGrepEvidenceSurveyOverflow(t *testing.T) {
	overflow := `{"distribution":[{"dir":".","count":200}],"truncated":true,"total":200}`
	if !GrepEvidenceSurvey(map[string]any{"pattern": "x"}, overflow) {
		t.Fatal("overflow grep should be survey")
	}
}

func TestGrepEvidenceSurveyLiteral(t *testing.T) {
	literal := `{"matches":[{"path":"a.go","line":1,"content":"x"}]}`
	if GrepEvidenceSurvey(map[string]any{"pattern": "x"}, literal) {
		t.Fatal("literal grep should not be survey")
	}
	if GrepEvidenceSurvey(map[string]any{"pattern": "x", "offset": 0}, overflowJSON()) {
		t.Fatal("offset grep should not be survey")
	}
	if GrepEvidenceSurvey(map[string]any{"pattern": "x", "max_matches": 50}, overflowJSON()) {
		t.Fatal("scoped max_matches should not be survey")
	}
	if GrepEvidenceSurvey(map[string]any{"pattern": "x", "max_matches": 200}, overflowJSON()) {
		t.Fatal("explicit max_matches at host cap should not be survey")
	}
}

func overflowJSON() string {
	return `{"distribution":[{"dir":".","count":200}],"truncated":true}`
}

func TestFindEvidenceSurveyOverflow(t *testing.T) {
	overflow := `{"distribution":[{"kind":"ext","key":".txt","count":500}],"truncated":true,"total":550}`
	if !FindEvidenceSurvey(map[string]any{"type": "file"}, overflow) {
		t.Fatal("overflow find should be survey")
	}
}

func TestFindEvidenceSurveyLiteral(t *testing.T) {
	literal := `{"results":[{"path":"a.txt","type":"file"}]}`
	if FindEvidenceSurvey(map[string]any{"type": "file"}, literal) {
		t.Fatal("literal find should not be survey")
	}
	overflow := `{"distribution":[{"kind":"ext","key":".txt","count":500}],"truncated":true,"total":550}`
	if FindEvidenceSurvey(map[string]any{"path": "docs", "type": "file"}, overflow) {
		t.Fatal("concrete subpath should not be survey")
	}
	if FindEvidenceSurvey(map[string]any{"name_glob": "fixture_*.md"}, overflow) {
		t.Fatal("name_glob should not be survey")
	}
}
