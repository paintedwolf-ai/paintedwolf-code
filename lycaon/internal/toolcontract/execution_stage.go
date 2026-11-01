package toolcontract

// ExecutionStage is bound execution-plane data shared by admission, dispatch
// and detection. Argv is decoded data, never reparsed shell text. Displays are
// canonical projections of that same stage and connected pipeline, not evidence.
// The retaining execution caller meters and freezes these values for its lifetime.
type ExecutionStage struct {
	ExecutionID         string
	OperationID         string
	Argv                []string
	CommandLine         string
	PipelineCommandLine string
}

// CloneExecutionStages detaches stage argv at an invocation boundary. The caller
// includes these retained bytes in its existing program memory reservation.
func CloneExecutionStages(stages []ExecutionStage) []ExecutionStage {
	result := append([]ExecutionStage(nil), stages...)
	for index := range result {
		result[index].Argv = append([]string(nil), result[index].Argv...)
	}
	return result
}
