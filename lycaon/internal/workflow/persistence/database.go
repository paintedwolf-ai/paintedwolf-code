package persistence

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func New(database db.Handle) *runstate.Repository {
	tx := &Transactions{db: database, queries: db.New(database)}
	runs := &Runs{transactions: tx}
	state := &State{transactions: tx}
	starts := &Starts{transactions: tx}
	commands := &Commands{transactions: tx}
	blueprints := &Blueprints{transactions: tx}
	teardowns := &Teardowns{transactions: tx}
	verdicts := &Verdicts{transactions: tx}
	assignments := &Assignments{transactions: tx}
	starts.runs = runs
	commands.runs = runs
	return &runstate.Repository{Assignments: assignments, Runs: runs, State: state, Starts: starts, Commands: commands, Blueprints: blueprints, Teardowns: teardowns, Verdicts: verdicts, Transactions: tx}
}
