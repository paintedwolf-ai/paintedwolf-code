package guard

// Rejection carries the code a caller branches on.
type Rejection struct{ Code string }

func Reject() Rejection { return Rejection{Code: "LIVE_CODE"} }

func Fire() string { return emit("anchor.fired") }

func emit(anchor string) string { return anchor }

// Seen compares; comparing a code does not emit it.
func Seen(code string) bool { return code == "ONLY_COMPARED" }

func ToolOnly() Rejection { return Rejection{Code: "TOOL_ONLY_CODE"} }

func unreached() Rejection { return Rejection{Code: "DEAD_CODE"} }
