import type { ShellFactState } from "../../contributions/shell-facts.ts";
import type { filesCommandContext } from "../../files/commands/files-command-context.ts";
import { createEffect, createMemo, onCleanup, onMount } from "solid-js";
import type { ContributionCommand } from "../../api/types.ts";
import { runComposerPrefillEffect } from "../../chat/composer/add-to-chat.ts";
import { contributionCommand, dispatchContributionCommand, registerUIEffectSink, registerCommandContextProvider, reportDispatchFailure, crossbarCommands } from "../../contributions/dispatch.ts";
import { setShellFactState } from "../../contributions/shell-facts.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { attachAppMenuCommands, invalidateAppMenuProjection, syncAppMenu } from "../../platform/desktop/app-menu.ts";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { registerHostCommandInvoker } from "../../shortcuts/dispatcher.ts";

export function createShellContributions(options: {
  projectId: () => string | null;
  sessionId: () => string | null;
  navigate: (destination: string) => void;
  editorContext: () => ReturnType<typeof filesCommandContext>["context"] | undefined;
  facts: () => ShellFactState;
  openCommand: (commandId: string) => void;
  windowFocused: () => boolean;
}) {
  onMount(() => {
    const unregisterEffects = registerUIEffectSink({
      navigate: options.navigate,
      composerPrefill: (text) => {
        void runComposerPrefillEffect(options.projectId(), text);
      },
    });
    onCleanup(unregisterEffects);
  });
  onMount(() => {
    onCleanup(registerCommandContextProvider((projectId) =>
      projectId === options.projectId() ? options.editorContext() : undefined,
    ));
  });
  createEffect(() => {
    setShellFactState(options.facts());
  });
  // Crossbar commands read the published fact state.
  const crossbarActions = createMemo<ContributionCommand[]>(() => {
    return crossbarCommands();
  });
  onMount(() => {
    const dispatchDeps = () => ({
      client: getLycaonClient(),
      projectId: options.projectId(),
      sessionId: options.sessionId(),
    });
    const activateContribution = (commandId: string) => {
      const command = contributionCommand(commandId);
      if (command && (command.input?.length || command.interaction || command.result_treatment === "output")) {
        options.openCommand(commandId);
        return;
      }
      const projectId = options.projectId();
      void dispatchContributionCommand(commandId, dispatchDeps()).then((result) =>
        reportDispatchFailure(result, projectId, command?.title ?? commandId),
      );
    };
    // Menus and shortcuts share command forms and result presentation.
    onCleanup(attachAppMenuCommands(activateContribution));
    onCleanup(registerHostCommandInvoker(activateContribution));
  });
  createEffect(() => {
    // The focused window supplies the app menu context.
    if (!options.windowFocused()) {
      invalidateAppMenuProjection();
      return;
    }
    const overrides = shortcutOverrides();
    void syncAppMenu(Object.keys(overrides).length > 0 ? overrides : null);
  });

  return { crossbarActions };
}
