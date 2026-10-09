package runstate

type CommandMutation struct {
	OperationID string
	Kind        string
	InputDigest string
	ProjectDir  string
	Vars        map[string]any
	Messages    []api.Message
	Posture     api.SessionPosture
	Workers     WorkerMutation
	Teardown    *TeardownIntent
	Rejection   *PhaseGateUnmetError
}

type WorkerMutation struct {
	HoldPending   bool
	CancelRunning bool
	CancelAll     bool
	ReleaseHeld   bool
}

type CommandRejection struct {
	Kind       string   `json:"kind"`
	Phase      string   `json:"phase"`
	Reason     string   `json:"reason"`
	FailedGate string   `json:"failed_gate"`
	Leaves     []string `json:"failed_leaves"`
}

type StartMutation struct {
	ProjectDir                string
	Vars                      map[string]any
	Messages                  []api.Message
	Posture                   api.SessionPosture
	AllowReplacementRebase    bool
	ReplacementTeardowns      map[string]*TeardownIntent
	ReplacementRebaseAttempts int
}

type ChildStartMutation struct {
	ProjectDir string
	Vars       map[string]any
	Messages   []api.Message
	Posture    api.SessionPosture
}
