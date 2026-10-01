package sourceview

import "fmt"

// Limitation identifies a source region whose executable context could not be established.
type Limitation struct {
	File      string
	Construct string
	Detail    string
	Start     Position
}

func (l *Limitation) Error() string {
	return fmt.Sprintf("%s: %s", l.Construct, l.Detail)
}
