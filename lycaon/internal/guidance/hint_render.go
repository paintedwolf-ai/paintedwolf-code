package guidance

// RenderHintFields renders what/why/fix for a hint from its registered fields.
func RenderHintFields(code string, entry HintEntry, data map[string]any) (what, why, fix string) {
	view := NormalizeRejectCodeView(code, entry)
	return renderFieldTemplate(view.What, data),
		renderFieldTemplate(view.Cause, data),
		renderFieldTemplate(view.Fix, data)
}
