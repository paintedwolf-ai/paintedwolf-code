import type { LycaonClient } from "../api/client.ts";
import { confirmAndOpenExternalLink } from "../platform/desktop/external-link.ts";
import { noticeReporterFor } from "../platform/connection/app-connection.ts";
import { APP_SCOPE, projectScope } from "../notices/notice-scope.ts";
import { contributionFrame } from "./contribution-store.ts";
import type {
  CommandInvokeContext,
  CommandUIEffect,
  ContributionCommand,
} from "../api/types.ts";
import { invokeCommand } from "../shortcuts/dispatcher.ts";
import { isNativeUIHandlerId } from "./native-ui-handlers.generated.ts";
import { evaluateCondition } from "./conditions.ts";
import { liveShellFactLookup } from "./shell-facts.ts";

export type UIEffectSink = {
  navigate: (destination: string) => void;
  composerPrefill: (text: string) => void;
};

let effectSink: UIEffectSink | null = null;
let contextProvider: ((projectId: string) => CommandInvokeContext | undefined) | null = null;

export function registerCommandContextProvider(provider: NonNullable<typeof contextProvider>): () => void {
  contextProvider = provider;
  return () => {
    if (contextProvider === provider) contextProvider = null;
  };
}

export function commandInvokeContext(projectId: string | null): CommandInvokeContext | undefined {
  return projectId ? contextProvider?.(projectId) : undefined;
}

export function registerUIEffectSink(sink: UIEffectSink): () => void {
  effectSink = sink;
  return () => {
    if (effectSink === sink) effectSink = null;
  };
}

export type DispatchDeps = {
  client: LycaonClient | null;
  projectId: string | null;
  sessionId: string | null;
};

export type DispatchResult =
  | { ok: true; command: ContributionCommand; output?: string; messageId?: string }
  | { ok: false; reason: DispatchFailure; message?: string };

export type DispatchFailure =
  | "no_frame"
  | "not_in_frame"
  | "handler_missing"
  | "declined"
  | "no_client"
  | "no_session"
  | "no_project"
  | "input_required"
  | "invoke_failed";

/**
 * Surface a failed fire-and-forget dispatch as a notice; palette rows, app
 * menus, and keyboard chords share this lane.
 */
export function reportDispatchFailure(
  result: DispatchResult,
  projectId: string | null,
  title: string,
): void {
  const message = dispatchFailureMessage(result);
  if (!message) return;
  noticeReporterFor(projectId ? projectScope(projectId) : APP_SCOPE).publish({
    severity: "warning",
    title,
    message,
  });
}

/** User-facing copy for a failed dispatch; Crossbar and menus share it. */
export function dispatchFailureMessage(result: DispatchResult): string | null {
  if (result.ok) return null;
  switch (result.reason) {
    case "declined":
      return "The command isn't applicable right now.";
    case "handler_missing":
      return "Nothing is mounted to run this command here.";
    case "no_session":
      return "Open a chat to run this command.";
    case "no_project":
      return "Open a project to run this command.";
    case "no_client":
      return "The engine connection is down.";
    case "input_required":
      return "This command needs input before it can run.";
    case "no_frame":
    case "not_in_frame":
      return "The command is no longer available.";
    case "invoke_failed":
      return result.message ?? "Command invocation failed.";
  }
}

export async function dispatchContributionCommand(
  commandId: string,
  deps: DispatchDeps,
  context?: CommandInvokeContext,
  args?: Record<string, unknown>,
): Promise<DispatchResult> {
  const frame = contributionFrame();
  if (!frame) return { ok: false, reason: "no_frame" };
  const command = frame.commands.find((row) => row.id === commandId);
  if (!command) return { ok: false, reason: "not_in_frame" };
  if (!args && (command.input?.length || command.interaction)) {
    return { ok: false, reason: "input_required" };
  }

  if (command.executor === "den") {
    const handlerId = command.handler_id ?? "";
    if (!isNativeUIHandlerId(handlerId)) {
      return { ok: false, reason: "handler_missing" };
    }
    // A throwing handler is a failed invocation, not an unhandled rejection
    // that wedges the caller's flow.
    try {
      switch (invokeCommand(handlerId)) {
        case "missing":
          return { ok: false, reason: "handler_missing" };
        case "declined":
          return { ok: false, reason: "declined" };
        case "ran":
          return { ok: true, command };
      }
    } catch (err) {
      return {
        ok: false,
        reason: "invoke_failed",
        message: err instanceof Error ? err.message : String(err),
      };
    }
  }

  if (!deps.client) return { ok: false, reason: "no_client" };
  const invokeContext = context ?? commandInvokeContext(deps.projectId);
  const req = {
    operation_id: crypto.randomUUID(),
    frame_revision: frame.frame_revision,
    ...(invokeContext ? { context: invokeContext } : {}),
    ...(args ? { args } : {}),
  };
  try {
    let response;
    if (command.invocation === "session") {
      if (!deps.sessionId) return { ok: false, reason: "no_session" };
      response = await deps.client.invokeSessionCommand(deps.sessionId, command.id, req);
    } else {
      if (!deps.projectId) return { ok: false, reason: "no_project" };
      response = await deps.client.invokeProjectCommand(deps.projectId, command.id, req);
    }
    if (response.status === "failed") {
      return {
        ok: false,
        reason: "invoke_failed",
        message: response.error?.message ?? "Command invocation failed",
      };
    }
    if (response.ui_effect) await applyUIEffect(response.ui_effect);
    return {
      ok: true,
      command,
      ...(response.output ? { output: response.output } : {}),
      ...(response.message_id ? { messageId: response.message_id } : {}),
    };
  } catch (err) {
    return {
      ok: false,
      reason: "invoke_failed",
      message: err instanceof Error ? err.message : String(err),
    };
  }
}

async function applyUIEffect(effect: CommandUIEffect): Promise<void> {
  switch (effect.kind) {
    case "navigate":
      if (effect.destination) effectSink?.navigate(effect.destination);
      return;
    case "composer_prefill":
      if (effect.text) effectSink?.composerPrefill(effect.text);
      return;
    case "external_link":
      if (effect.url) await confirmAndOpenExternalLink(effect.url);
      return;
    default:
      return;
  }
}

export function contributionCommandAvailable(commandId: string): boolean {
  const frame = contributionFrame();
  if (!frame) return false;
  const command = frame.commands.find((row) => row.id === commandId);
  if (!command) return false;
  const lookup = liveShellFactLookup();
  if (!lookup) return false;
  return evaluateCondition(command.when ?? null, lookup) &&
    evaluateCondition(command.enablement ?? null, lookup);
}

export function contributionCommandRelevant(command: ContributionCommand): boolean {
  const lookup = liveShellFactLookup();
  return !!lookup && evaluateCondition(command.when ?? null, lookup);
}

export function contributionCommandEnabled(command: ContributionCommand): boolean {
  const lookup = liveShellFactLookup();
  return !!lookup && evaluateCondition(command.enablement ?? null, lookup);
}

export function contributionCommand(commandId: string): ContributionCommand | null {
  return contributionFrame()?.commands.find((row) => row.id === commandId) ?? null;
}

export function nativeCommandId(handlerId: string): string | null {
  const frame = contributionFrame();
  if (!frame) return null;
  return frame.commands.find((row) => row.handler_id === handlerId)?.id ?? null;
}

export function contributionCommands(): readonly ContributionCommand[] {
  return contributionFrame()?.commands ?? [];
}

export function crossbarCommands(): ContributionCommand[] {
  const frame = contributionFrame();
  if (!frame) return [];
  const lookup = liveShellFactLookup();
  if (!lookup) return [];
  return frame.commands.filter(
    (command) =>
      command.palette !== false && evaluateCondition(command.when ?? null, lookup),
  );
}
