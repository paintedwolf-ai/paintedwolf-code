package toolcontract

// ProducerFailure carries the domain's known effect and recovery evidence
// through an error boundary. Consumers retain it instead of assuming no effect.
type ProducerFailure interface {
	error
	ProducerResult() ProducerResult
}

const AcceptedUnpublishedWrite = "accepted_unpublished_write"

// OutputWithheld records staged bytes discarded before destination publication.
const OutputWithheld = "output_withheld"
