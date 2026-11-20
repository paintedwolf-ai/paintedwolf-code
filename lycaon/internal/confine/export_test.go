package confine

import (
	"os"
)

// LineageMarkerForTest returns the descendant marker a confined launch hands its
// child, so a test can start a descendant without a full spawn.
func LineageMarkerForTest(c *Confinement) *os.File {
	if c == nil {
		return nil
	}
	return c.lineage.ChildFile()
}

// BuildSeatbeltProfile renders the validated profile for assertions.
func BuildSeatbeltProfile(c Confinement) (string, error) {
	c, err := validateProfileInputs(c)
	if err != nil {
		return "", err
	}
	return renderSeatbeltProfile(c)
}
