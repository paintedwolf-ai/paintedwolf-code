package spawn

import (
	"github.com/lycaon/lycaon/pkg/api"
	"unicode/utf8"
)

// MaxTaskCharterRunes bounds the assignment retained from plan through dispatch.
const MaxTaskCharterRunes = 4096

func TaskCharterRunes(charter api.WorkerTaskCharter) int {
	total := utf8.RuneCountInString(charter.Goal)
	for _, values := range [][]string{charter.KnownFacts, charter.Constraints, charter.DoneWhen, charter.ContextRefs} {
		for _, value := range values {
			total += utf8.RuneCountInString(value)
		}
	}
	return total
}
