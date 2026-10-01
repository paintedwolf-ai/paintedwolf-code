package tools

// SetDisplaySubject retains the readable target for durable screening and presentation.
func (c ToolContext) SetDisplaySubject(subject string) {
	if c.Out != nil {
		c.Out.DisplaySubject = subject
	}
}
