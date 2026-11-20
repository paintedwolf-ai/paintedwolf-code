package app

import (
	"context"
	"fmt"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
)

func (b *serveBuilder) wireWaitRecovery(subscriptions *loopwake.WaitSubscriptions, store *awaitstore.Store) error {
	leases := bootrecovery.Entry{
		Name: "agent-wait-leases", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: func(ctx context.Context) error { return loopwake.RecoverWaitLeases(ctx, subscriptions, store) },
	}
	if commands := b.execution.Host.Commands; commands != nil {
		commands.SetProgramParking(loopwake.ProgramParking{Subscriptions: subscriptions})
		commands.SetProgramEffects(b.sessions.Manager.Runner.Transcript)
		subscriptions.SetLostProgramResult(commands.ProgramReceiptResult)
		publications := bootrecovery.Entry{
			Name: "command-output-publications", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
			After: []string{"tool-invocations"},
			Run: func(ctx context.Context) error {
				return commands.RecoverProgramPublications(ctx, b.programOutputDirectory)
			},
		}
		if err := b.registerRecovery(publications); err != nil {
			return err
		}
		leases.After = []string{publications.Name}
	}
	return b.registerRecovery(leases)
}

func (b *serveBuilder) programOutputDirectory(ctx context.Context, sessionID string) (string, error) {
	sess, err := b.storage.Sessions.Sessions.Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if sess == nil || sess.ID != sessionID {
		return "", fmt.Errorf("command output session %q is unavailable", sessionID)
	}
	output := b.sessions.Manager.Runner.Execution.SessionDirectories(ctx, sess).Output
	if output == "" {
		return "", fmt.Errorf("command output directory for session %q is unavailable", sessionID)
	}
	return output, nil
}
