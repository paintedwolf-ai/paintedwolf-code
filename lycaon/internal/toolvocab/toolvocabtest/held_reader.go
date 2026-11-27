package toolvocabtest

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ReaderHolding is a declared one-root Reader offering the tools held marks
// true. Command reach marks in held set the command profile's effect classes.
func ReaderHolding(held map[string]bool) (prompts.Reader, error) {
	profileID := "worker"
	if held["task"] {
		profileID = prompts.CoordinatorProfileID
	}
	tools := make(map[string]bool, len(held))
	var offered []string
	for name, ok := range held {
		if !ok || isReachMark(name) {
			continue
		}
		tools[name] = true
		offered = append(offered, name)
	}
	sort.Strings(offered)
	var effects toolcontract.CommandEffects
	if tools["command"] {
		classes := []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}
		if held[toolcontract.ReachCommandMutation] {
			classes = append(classes, toolcontract.CommandMutation)
		}
		if held[toolcontract.ReachCommandExternalExec] {
			classes = append(classes, toolcontract.CommandExternalExec)
		}
		if held[toolcontract.ReachCommandNetworkEgress] {
			classes = append(classes, toolcontract.CommandNetworkEgress)
		}
		var err error
		if effects, err = toolcontract.NewCommandEffects(classes...); err != nil {
			return prompts.Reader{}, err
		}
	}
	return prompts.NewReader(prompts.HostFacts{
		Profiles:              []sandbox.ToolProfile{{ID: profileID, Tools: tools, CommandEffects: effects}},
		RootCount:             1,
		HasGit:                true,
		WebSearch:             true,
		CommandExternalHidden: held[toolcontract.ReachCommandExternalExec] && !held[toolcontract.ReachCommandExternalExecSelector],
	}, prompts.TurnProjection{ProfileID: profileID, Offered: offered, Declared: true})
}

func isReachMark(name string) bool {
	switch name {
	case toolcontract.ReachCommandMutation, toolcontract.ReachCommandExternalExec,
		toolcontract.ReachCommandExternalExecSelector, toolcontract.ReachCommandNetworkEgress:
		return true
	}
	return strings.HasPrefix(name, toolcontract.ReachCommandDeniedPrefix)
}
