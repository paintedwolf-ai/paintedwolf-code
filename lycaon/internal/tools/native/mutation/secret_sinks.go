package mutation

import "github.com/lycaon/lycaon/internal/commandprogram"

// SecretSinkOptions maps each form's value options to the contract's
// secret_reference_args. Every other argv word stays outside the declared sinks.
var SecretSinkOptions = map[string]map[string]string{
	"edit":          {"--old-string": "old_string", "--new-string": "new_string"},
	"replace-lines": {"--content": "new_content"},
}

// ReviewedArguments is the reviewed input shape for a form: sink options move to
// their named slots, where the host screens and resolves them.
func ReviewedArguments(review commandprogram.ArgvReview) map[string]any {
	sinks := SecretSinkOptions[review.Command]
	if len(sinks) == 0 {
		return review.Arguments()
	}
	slots := map[string]any{}
	options := make([]commandprogram.ReviewedOption, 0, len(review.Options))
	for i := len(review.Options) - 1; i >= 0; i-- {
		option := review.Options[i]
		slot, sink := sinks[option.Option]
		if _, taken := slots[slot]; !sink || taken || len(option.Values) != 1 {
			options = append(options, option)
			continue
		}
		slots[slot] = option.Values[0]
	}
	for left, right := 0, len(options)-1; left < right; left, right = left+1, right-1 {
		options[left], options[right] = options[right], options[left]
	}
	review.Options = options
	arguments := review.Arguments()
	for slot, value := range slots {
		arguments[slot] = value
	}
	return arguments
}
