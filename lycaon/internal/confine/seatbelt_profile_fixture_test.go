package confine

// BuildSeatbeltProfile renders the validated profile for assertions.
func BuildSeatbeltProfile(c Confinement) (string, error) {
	c, err := validateProfileInputs(c)
	if err != nil {
		return "", err
	}
	return renderSeatbeltProfile(c)
}
