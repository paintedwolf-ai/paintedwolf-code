package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestListEvidenceSurvey_unboundedMapSurvey(t *testing.T) {
	args := map[string]any{"path": "."}
	content := `{"path":".","view":"map","tree":{"path":".","type":"dir"},"selected":0,"total":1}`
	if !evidence.ListEvidenceSurvey(args, content) {
		t.Fatal("unbounded map list_dir should be survey")
	}
}

func TestListEvidenceSurvey_targetedNonSurvey(t *testing.T) {
	args := map[string]any{"path": ".", "max_depth": 1}
	content := `{"path":".","entries":[{"name":"a.go","type":"file","mode":"644"}],"total_entries":1,"truncated":false}`
	if evidence.ListEvidenceSurvey(args, content) {
		t.Fatal("targeted list_dir should not be survey")
	}
}
