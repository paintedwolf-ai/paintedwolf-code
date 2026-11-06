package mutation

// secretSinkOptions maps each form's value options to the contract's
// secret_reference_args. Every other argv word stays outside the declared sinks.
var secretSinkOptions = map[string]map[string]string{
	"edit":          {"--old-string": "old_string", "--new-string": "new_string"},
	"replace-lines": {"--content": "new_content"},
}
