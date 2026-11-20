package worker

import "errors"

var ErrNoPendingJobs = errors.New("no pending worker jobs")
var ErrMaxWorkers = errors.New("max workers reached")

const MaxWorkers = 48
