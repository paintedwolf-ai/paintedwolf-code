package structrewrite

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func sourceAnalysisRecovered(operation, language string, sourceBytes int, recovered any) *tsparse.Failure {
	if language == "" {
		language = "unknown"
	}
	cause := fmt.Errorf("%s panicked: %v", operation, recovered)
	return &tsparse.Failure{Reason: "panic", Language: language, SourceBytes: sourceBytes, Cause: cause, Detail: cause.Error()}
}
