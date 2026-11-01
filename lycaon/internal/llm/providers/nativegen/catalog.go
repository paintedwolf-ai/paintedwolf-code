package nativegen

import (
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

const (
	// DefaultModelID identifies the bundled Gemma 4 E4B model.
	DefaultModelID = "gemma4-e4b-paintedwolf"
	// DefaultContextWindowTokens is the 64K total token window shared by coordinator and summarizer roles.
	DefaultContextWindowTokens = 65536
	// DefaultOutputReserveTokens is the default output token reserve when MaxTokens is unset.
	DefaultOutputReserveTokens = 2048
	// MaxOutputReserveTokens bounds requested completion tokens on the local model.
	MaxOutputReserveTokens = 4096
)

// ErrContextCapacityExceeded indicates that prompt tokens plus output reserve exceed the 64K window.
var ErrContextCapacityExceeded = errors.New("nativegen: request exceeds 64K context capacity")

// DefaultModelEntry returns the catalog descriptor for the bundled Gemma 4 E4B model.
func DefaultModelEntry() modelinfo.Entry {
	return modelinfo.Entry{
		ID:            DefaultModelID,
		ContextLength: DefaultContextWindowTokens,
		MaxTokens:     MaxOutputReserveTokens,
		Callable:      modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
		Capabilities: modelinfo.ModelCapabilities{
			Chat:             modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Streaming:        modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Tools:            modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			StructuredOutput: modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			PromptCaching:    modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Vision:           modelinfo.Evidence(modelinfo.CapabilityUnsupported, "nativegen"),
		},
	}
}
