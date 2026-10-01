package tooloutput

// ScreenedOutput holds tool output after durable secret screening.
type ScreenedOutput struct {
	text string
}

// Screened admits bytes that have crossed the durable secret screen.
func Screened(text string) ScreenedOutput {
	return ScreenedOutput{text: text}
}

// String returns the screened bytes.
func (o ScreenedOutput) String() string { return o.text }
