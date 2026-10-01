package api

// TurnLoadTrigger names what asked the decision engine.
type TurnLoadTrigger string

const (
	// TurnLoadTriggerTurn is the decision before a turn's first model call.
	TurnLoadTriggerTurn TurnLoadTrigger = "turn"
	// TurnLoadTriggerRequest resolves request_tools text.
	TurnLoadTriggerRequest TurnLoadTrigger = "request"
	// TurnLoadTriggerLookup resolves skills_read text.
	TurnLoadTriggerLookup TurnLoadTrigger = "lookup"
	// TurnLoadTriggerToolEvent is the skill read a turn's first loadable tool call asked for.
	TurnLoadTriggerToolEvent TurnLoadTrigger = "tool_event"
)

// TurnLoadMatchBy names how a request or lookup resolved its text.
type TurnLoadMatchBy string

const (
	// TurnLoadMatchByEngine means the engine ranked the candidates.
	TurnLoadMatchByEngine TurnLoadMatchBy = "engine"
	// TurnLoadMatchByName means the text spelled the names out.
	TurnLoadMatchByName TurnLoadMatchBy = "name"
	// TurnLoadMatchByNone means nothing matched.
	TurnLoadMatchByNone TurnLoadMatchBy = "none"
)

// TurnLoadToolSource names why a tool stands loaded.
type TurnLoadToolSource string

const (
	// TurnLoadToolSourcePredicted means a turn decision predicted the tool.
	TurnLoadToolSourcePredicted TurnLoadToolSource = "predicted"
	// TurnLoadToolSourceRequested means the model asked for it through request_tools.
	TurnLoadToolSourceRequested TurnLoadToolSource = "requested"
	// TurnLoadToolSourceCompanion means it loaded with a predicted tool that declares it.
	TurnLoadToolSourceCompanion TurnLoadToolSource = "companion"
)

// TurnLoadCacheState names whether the provider still held the chat's
// standing prompt prefix when a turn opened.
type TurnLoadCacheState string

const (
	TurnLoadCacheStateWarm TurnLoadCacheState = "warm"
	TurnLoadCacheStateCold TurnLoadCacheState = "cold"
)

// TurnLoadColdReason names the recorded fact that made a turn cold.
type TurnLoadColdReason string

const (
	TurnLoadColdReasonFirstTurn    TurnLoadColdReason = "first_turn"
	TurnLoadColdReasonUncached     TurnLoadColdReason = "uncached"
	TurnLoadColdReasonIdle         TurnLoadColdReason = "idle"
	TurnLoadColdReasonModelChanged TurnLoadColdReason = "model_changed"
	TurnLoadColdReasonNotResident  TurnLoadColdReason = "not_resident"
)
